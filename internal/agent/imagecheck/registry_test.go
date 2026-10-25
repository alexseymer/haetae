package imagecheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

func TestRegistryLookupTokenIndexAndLimits(t *testing.T) {
	configJSON := []byte(`{"created":"2026-01-01T00:00:00Z"}`)
	configDigest := "sha256:" + sha256hex(configJSON)
	manifest := []byte(fmt.Sprintf(
		`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"digest":%q,"size":%d},"layers":[]}`,
		configDigest, len(configJSON)))
	manifestDigest := "sha256:" + sha256hex(manifest)
	index := []byte(fmt.Sprintf(
		`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.list.v2+json","manifests":[{"digest":%q,"platform":{"os":"linux","architecture":"amd64"}},{"digest":"sha256:%s","platform":{"os":"linux","architecture":"arm64"}}]}`,
		manifestDigest, strings.Repeat("b", 64)))

	var sawToken bool
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		sawToken = true
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-token"})
	})
	mux.HandleFunc("/v2/library/hello/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+`/token",service="registry",scope="repository:library/hello:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.list.v2+json")
		w.Header().Set("Docker-Content-Digest", "sha256:"+sha256hex(index))
		_, _ = w.Write(index)
	})
	mux.HandleFunc("/v2/library/hello/manifests/"+manifestDigest, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.Header().Set("Docker-Content-Digest", manifestDigest)
		_, _ = w.Write(manifest)
	})
	mux.HandleFunc("/v2/library/hello/blobs/"+configDigest, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(configJSON)
	})

	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")

	c := &RegistryClient{HTTP: srv.Client(), Timeout: 5 * time.Second}
	info, err := c.lookupHost(context.Background(), host, "library/hello", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if !sawToken {
		t.Fatal("expected bearer token flow")
	}
	if info.Digest != manifestDigest {
		t.Fatalf("digest=%s want %s", info.Digest, manifestDigest)
	}
	if info.CreatedAt.Year() != 2026 {
		t.Fatalf("created=%v", info.CreatedAt)
	}
}

func TestRegistryRefusesHTTPRedirect(t *testing.T) {
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer httpSrv.Close()

	httpsMux := http.NewServeMux()
	httpsMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpSrv.URL, http.StatusFound)
	})
	httpsSrv := httptest.NewTLSServer(httpsMux)
	defer httpsSrv.Close()

	c := &RegistryClient{
		HTTP: &http.Client{
			Transport: httpsSrv.Client().Transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Scheme != "https" {
					return fmt.Errorf("registry: refusing redirect to non-HTTPS")
				}
				return nil
			},
		},
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, httpsSrv.URL+"/", nil)
	resp, err := c.client().Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("want non-HTTPS refusal, got %v", err)
	}
}

func TestRegistrySizeLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/library/big/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		_, _ = w.Write(make([]byte, maxManifestBytes+10))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	c := &RegistryClient{HTTP: srv.Client(), Timeout: time.Second}
	_, err := c.lookupHost(context.Background(), host, "library/big", "latest")
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("want too large, got %v", err)
	}
}

func TestParseRefDockerHub(t *testing.T) {
	host, repo, tag, err := parseRef("busybox")
	if err != nil || host != "registry-1.docker.io" || repo != "library/busybox" || tag != "latest" {
		t.Fatalf("got %s %s %s %v", host, repo, tag, err)
	}
	host, repo, tag, err = parseRef("user/app:1.2")
	if err != nil || host != "registry-1.docker.io" || repo != "user/app" || tag != "1.2" {
		t.Fatalf("got %s %s %s %v", host, repo, tag, err)
	}
}

func TestPullRefusesDigestMismatchAndYoung(t *testing.T) {
	now := time.Now().UTC()
	configJSON, _ := json.Marshal(map[string]string{"created": now.Format(time.RFC3339Nano)})
	configDigest := "sha256:" + sha256hex(configJSON)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":%q},"layers":[]}`, configDigest))
	manifestDigest := "sha256:" + sha256hex(manifest)

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/library/app/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.Header().Set("Docker-Content-Digest", manifestDigest)
		_, _ = w.Write(manifest)
	})
	mux.HandleFunc("/v2/library/app/blobs/"+configDigest, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(configJSON)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")

	chk := &Checker{
		Registry: &RegistryClient{HTTP: srv.Client(), Timeout: time.Second},
		Now:      func() time.Time { return now },
	}

	wrong := "sha256:" + strings.Repeat("a", 64)
	err := chk.pullHost(context.Background(), host, "library/app", "latest", &agentv1.PullImage{
		Ref: "app:latest", Digest: wrong, MinAgeHours: 48,
	})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("want digest mismatch, got %v", err)
	}

	err = chk.pullHost(context.Background(), host, "library/app", "latest", &agentv1.PullImage{
		Ref: "app:latest", Digest: manifestDigest, MinAgeHours: 48,
	})
	if err == nil || !strings.Contains(err.Error(), "too young") {
		t.Fatalf("want too young, got %v", err)
	}

	err = chk.pullHost(context.Background(), host, "library/app", "latest", &agentv1.PullImage{
		Ref: "app:latest", Digest: manifestDigest, AllowYoung: true, MinAgeHours: 48,
	})
	if err == nil || !strings.Contains(err.Error(), "no docker") {
		t.Fatalf("want no docker (would pull), got %v", err)
	}
}

func (c *RegistryClient) lookupHost(ctx context.Context, host, repo, tag string) (ManifestInfo, error) {
	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", host, repo, tag)
	body, digest, contentType, err := c.getManifest(ctx, manifestURL, host, repo)
	if err != nil {
		return ManifestInfo{}, err
	}
	if strings.Contains(contentType, "manifest.list") || strings.Contains(contentType, "image.index") || isIndex(body) {
		d, err := selectPlatform(body)
		if err != nil {
			return ManifestInfo{}, err
		}
		childURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", host, repo, d)
		body, digest, contentType, err = c.getManifest(ctx, childURL, host, repo)
		if err != nil {
			return ManifestInfo{}, err
		}
		_ = contentType
	}
	if digest == "" {
		digest = "sha256:" + sha256hex(body)
	}
	created, _ := configCreated(ctx, c, host, repo, body)
	return ManifestInfo{Digest: digest, CreatedAt: created}, nil
}

func (c *Checker) pullHost(ctx context.Context, host, repo, tag string, cmd *agentv1.PullImage) error {
	info, err := c.Registry.lookupHost(ctx, host, repo, tag)
	if err != nil {
		return fmt.Errorf("pull: re-check: %w", err)
	}
	digest := strings.TrimSpace(cmd.GetDigest())
	if info.Digest != digest {
		return fmt.Errorf("pull: digest mismatch: registry has %s, expected %s", info.Digest, digest)
	}
	minAge := time.Duration(cmd.GetMinAgeHours()) * time.Hour
	if minAge <= 0 {
		minAge = 48 * time.Hour
	}
	if !cmd.GetAllowYoung() && !info.CreatedAt.IsZero() {
		age := c.Now().Sub(info.CreatedAt)
		if age < minAge {
			return fmt.Errorf("pull: image too young (%s < %s)", age.Round(time.Minute), minAge)
		}
	}
	if c.Docker == nil {
		return fmt.Errorf("pull: no docker client")
	}
	return nil
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
