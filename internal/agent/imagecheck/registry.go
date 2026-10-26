package imagecheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

const (
	maxManifestBytes = 4 << 20
	maxConfigBytes   = 1 << 20
	defaultTimeout   = 20 * time.Second
)

// RegistryClient talks to OCI/Docker Registry HTTP API V2 with anonymous
// bearer auth. No third-party registry SDK.
type RegistryClient struct {
	HTTP    *http.Client
	Timeout time.Duration
}

var guardedTransport = newGuardedTransport()

func (c *RegistryClient) client() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{
		Timeout:       defaultTimeout,
		Transport:     guardedTransport,
		CheckRedirect: checkRedirect,
	}
}

// checkRedirect allows plain HTTP only between loopback hosts (a local
// registry on 127.0.0.0/8, which Docker treats as insecure), never from an
// HTTPS registry down to HTTP. Address checks happen in guardedDial.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("registry: too many redirects")
	}
	if req.URL.Scheme == "https" {
		return nil
	}
	if req.URL.Scheme == "http" && via[0].URL.Scheme == "http" && registryScheme(req.URL.Host) == "http" {
		return nil
	}
	return errors.New("registry: refusing redirect to non-HTTPS")
}

func (c *RegistryClient) timeout() time.Duration {
	if c != nil && c.Timeout > 0 {
		return c.Timeout
	}
	return defaultTimeout
}

// ManifestInfo is the resolved digest and config creation time for a tag.
type ManifestInfo struct {
	Digest    string // sha256:...
	CreatedAt time.Time
}

// Lookup resolves ref (name:tag or name without tag → latest) to the current
// digest and config created time for the host architecture.
func (c *RegistryClient) Lookup(ctx context.Context, ref string) (ManifestInfo, error) {
	host, repo, tag, err := parseRef(ref)
	if err != nil {
		return ManifestInfo{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	ctx = withPrivateAllowance(ctx, privateRegistry(ctx, host))

	scheme := registryScheme(host)
	manifestURL := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, repo, url.PathEscape(tag))
	body, digest, contentType, err := c.getManifest(ctx, manifestURL, host, repo)
	if err != nil {
		return ManifestInfo{}, err
	}

	switch {
	case strings.Contains(contentType, "manifest.list") || strings.Contains(contentType, "image.index") || isIndex(body):
		d, err := selectPlatform(body)
		if err != nil {
			return ManifestInfo{}, err
		}
		childURL := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, repo, d)
		body, digest, contentType, err = c.getManifest(ctx, childURL, host, repo)
		if err != nil {
			return ManifestInfo{}, err
		}
		_ = contentType
	}

	if digest == "" {
		sum := sha256.Sum256(body)
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	created, err := configCreated(ctx, c, scheme, host, repo, body)
	if err != nil {
		return ManifestInfo{Digest: digest}, nil // digest still useful
	}
	return ManifestInfo{Digest: digest, CreatedAt: created}, nil
}

// registryScheme returns http for loopback registries (Docker's default
// insecure CIDR 127.0.0.0/8 and localhost), otherwise https.
func registryScheme(host string) string {
	h := host
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host, "]") {
		// host:port (not an IPv6 literal)
		h = host[:i]
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return "http"
	}
	return "https"
}

func (c *RegistryClient) getManifest(ctx context.Context, rawURL, host, repo string) (body []byte, digest, contentType string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/json",
	}, ", "))

	resp, err := c.doAuth(ctx, req, host, repo)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, "", "", fmt.Errorf("registry: manifest %s: status %d: %s", rawURL, resp.StatusCode, clip(string(limited), 200))
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	if len(body) > maxManifestBytes {
		return nil, "", "", errors.New("registry: manifest too large")
	}
	digest = resp.Header.Get("Docker-Content-Digest")
	return body, digest, resp.Header.Get("Content-Type"), nil
}

func (c *RegistryClient) doAuth(ctx context.Context, req *http.Request, host, repo string) (*http.Response, error) {
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	_ = resp.Body.Close()
	chal := resp.Header.Get("WWW-Authenticate")
	token, err := c.bearerToken(ctx, chal, repo)
	if err != nil {
		return nil, err
	}
	req2 := req.Clone(ctx)
	req2.Header.Set("Authorization", "Bearer "+token)
	return c.client().Do(req2)
}

func (c *RegistryClient) bearerToken(ctx context.Context, challenge, repo string) (string, error) {
	realm, service, scope := parseWWWAuth(challenge)
	if realm == "" {
		return "", errors.New("registry: no bearer realm in WWW-Authenticate")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" {
		return "", errors.New("registry: token realm must be HTTPS")
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	if scope != "" {
		q.Set("scope", scope)
	} else {
		q.Set("scope", "repository:"+repo+":pull")
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry: token status %d", resp.StatusCode)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", err
	}
	if tok.Token != "" {
		return tok.Token, nil
	}
	if tok.AccessToken != "" {
		return tok.AccessToken, nil
	}
	return "", errors.New("registry: empty token")
}

func parseWWWAuth(h string) (realm, service, scope string) {
	// Bearer realm="...",service="...",scope="..."
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return "", "", ""
	}
	rest := strings.TrimSpace(h[7:])
	for _, part := range splitAuthParts(rest) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "realm":
			realm = v
		case "service":
			service = v
		case "scope":
			scope = v
		}
	}
	return realm, service, scope
}

func splitAuthParts(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
			cur.WriteByte(s[i])
		case ',':
			if inQuote {
				cur.WriteByte(s[i])
			} else {
				parts = append(parts, strings.TrimSpace(cur.String()))
				cur.Reset()
			}
		default:
			cur.WriteByte(s[i])
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, strings.TrimSpace(cur.String()))
	}
	return parts
}

func parseRef(ref string) (host, repo, tag string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", "", errors.New("registry: empty ref")
	}
	if strings.Contains(ref, "@") {
		return "", "", "", errors.New("registry: digest-pinned refs have no movable tag")
	}
	tag = "latest"
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		// Distinguish registry:port from tag.
		slash := strings.LastIndex(ref, "/")
		if i > slash {
			tag = ref[i+1:]
			ref = ref[:i]
		}
	}
	switch {
	case !strings.Contains(ref, "/") && !strings.Contains(ref, "."):
		// busybox → registry-1.docker.io/library/busybox
		host = "registry-1.docker.io"
		repo = "library/" + ref
	case strings.HasPrefix(ref, "docker.io/"):
		host = "registry-1.docker.io"
		repo = strings.TrimPrefix(ref, "docker.io/")
		if !strings.Contains(repo, "/") {
			repo = "library/" + repo
		}
	case !strings.Contains(ref, ".") && !strings.Contains(ref, ":") && strings.Count(ref, "/") == 1:
		// user/name on Docker Hub
		host = "registry-1.docker.io"
		repo = ref
	default:
		slash := strings.Index(ref, "/")
		if slash < 0 {
			return "", "", "", fmt.Errorf("registry: bad ref %q", ref)
		}
		host, repo = ref[:slash], ref[slash+1:]
	}
	return host, repo, tag, nil
}

func isIndex(body []byte) bool {
	var m struct {
		Manifests []json.RawMessage `json:"manifests"`
	}
	return json.Unmarshal(body, &m) == nil && len(m.Manifests) > 0
}

func selectPlatform(body []byte) (digest string, err error) {
	var idx struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(body, &idx); err != nil {
		return "", err
	}
	goos, goarch := runtime.GOOS, runtime.GOARCH
	var fallback string
	for _, m := range idx.Manifests {
		if m.Platform.OS == goos && m.Platform.Architecture == goarch {
			return m.Digest, nil
		}
		if m.Platform.OS == "linux" && m.Platform.Architecture == goarch && fallback == "" {
			fallback = m.Digest
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	if len(idx.Manifests) > 0 {
		return idx.Manifests[0].Digest, nil
	}
	return "", errors.New("registry: empty index")
}

func configCreated(ctx context.Context, c *RegistryClient, scheme, host, repo string, manifest []byte) (time.Time, error) {
	var man struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(manifest, &man); err != nil || man.Config.Digest == "" {
		return time.Time{}, errors.New("registry: no config digest")
	}
	if scheme == "" {
		scheme = registryScheme(host)
	}
	blobURL := fmt.Sprintf("%s://%s/v2/%s/blobs/%s", scheme, host, repo, man.Config.Digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, blobURL, nil)
	if err != nil {
		return time.Time{}, err
	}
	resp, err := c.doAuth(ctx, req, host, repo)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("registry: config blob status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxConfigBytes+1))
	if err != nil {
		return time.Time{}, err
	}
	if len(body) > maxConfigBytes {
		return time.Time{}, errors.New("registry: config too large")
	}
	// Verify digest of what we fetched.
	want := strings.TrimPrefix(man.Config.Digest, "sha256:")
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != want {
		return time.Time{}, errors.New("registry: config digest mismatch")
	}
	var cfg struct {
		Created time.Time `json:"created"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return time.Time{}, err
	}
	return cfg.Created, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
