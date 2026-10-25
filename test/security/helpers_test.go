//go:build e2e_security

package security_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type inventoryDoc struct {
	ImageID string `json:"imageId"`
	Distro  struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"distro"`
	Packages []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Type    string `json:"type"`
	} `json:"packages"`
}

func runInventory(t *testing.T, bin, image string) inventoryDoc {
	t.Helper()
	cmd := exec.Command(bin, "--docker-image", image, "--out", "-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inventory %s: %v\n%s", image, err, out)
	}
	// Helper may write progress on stderr mixed; find JSON object.
	raw := out
	if i := bytes.IndexByte(out, '{'); i >= 0 {
		raw = out[i:]
	}
	var doc inventoryDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse inventory JSON: %v\n%s", err, out)
	}
	if len(doc.Packages) == 0 {
		t.Fatalf("inventory of %s returned no packages", image)
	}
	return doc
}

func pickAPKPackage(doc inventoryDoc) (name, version, major, minor string) {
	for _, p := range doc.Packages {
		if p.Type == "apk" && p.Name != "" && p.Version != "" {
			name, version = p.Name, p.Version
			break
		}
	}
	if name == "" {
		p := doc.Packages[0]
		name, version = p.Name, p.Version
	}
	ver := doc.Distro.Version
	parts := strings.SplitN(ver, ".", 3)
	major = "3"
	minor = "20"
	if len(parts) >= 1 && parts[0] != "" {
		major = parts[0]
	}
	if len(parts) >= 2 {
		minor = parts[1]
	}
	return name, version, major, minor
}

func writeFailStub(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fail-inventory")
	script := "#!/bin/sh\necho 'stub failure' >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSleepStub(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "sleep-inventory")
	script := "#!/bin/sh\nsleep 60\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// pushRegistryImage builds a tiny OCI image with a crafted created time and
// pushes it to a local registry via the registry HTTP API (no docker build).
func pushRegistryImage(t *testing.T, registryHost, repo, tag string, created time.Time, files map[string][]byte) (digest string) {
	t.Helper()
	layer, layerDigest, diffID := tarGzipLayer(t, files)
	config := map[string]any{
		"created":      created.UTC().Format(time.RFC3339Nano),
		"architecture": "amd64",
		"os":           "linux",
		"rootfs": map[string]any{
			"type":     "layers",
			"diff_ids": []string{diffID},
		},
		"config": map[string]any{
			"Env": []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
			"Cmd": []string{"/bin/true"},
		},
	}
	configJSON, _ := json.Marshal(config)
	configDigest := "sha256:" + sha256hex(configJSON)

	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.docker.distribution.manifest.v2+json",
		"config": map[string]any{
			"mediaType": "application/vnd.docker.container.image.v1+json",
			"size":      len(configJSON),
			"digest":    configDigest,
		},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
			"size":      len(layer),
			"digest":    layerDigest,
		}},
	}
	manifestJSON, _ := json.Marshal(manifest)
	digest = "sha256:" + sha256hex(manifestJSON)

	base := "http://" + registryHost + "/v2/" + repo
	putBlob(t, base+"/blobs/"+configDigest, configJSON)
	putBlob(t, base+"/blobs/"+layerDigest, layer)
	req, err := http.NewRequest(http.MethodPut, base+"/manifests/"+tag, bytes.NewReader(manifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("put manifest: %d %s", resp.StatusCode, body)
	}
	return digest
}

func putBlob(t *testing.T, url string, data []byte) {
	t.Helper()
	// Monolithic upload via POST ?digest=
	u := strings.Replace(url, "/blobs/sha256:", "/blobs/uploads/?digest=sha256:", 1)
	// Prefer: initiate upload then PUT
	resp, err := http.Post(strings.Split(url, "/blobs/")[0]+"/blobs/uploads/", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != 201 {
		// Fall back to monolithic if supported.
		req, _ := http.NewRequest(http.MethodPost, u, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/octet-stream")
		r2, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r2.Body.Close()
		if r2.StatusCode >= 300 {
			b, _ := io.ReadAll(r2.Body)
			t.Fatalf("blob upload fallback: %d %s", r2.StatusCode, b)
		}
		return
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("no upload Location")
	}
	if strings.HasPrefix(loc, "/") {
		// relative
		hostPart := strings.Split(url, "/v2/")[0]
		loc = hostPart + loc
	}
	digest := "sha256:" + strings.TrimPrefix(filepath.Base(url), "sha256:")
	if !strings.Contains(loc, "digest=") {
		if strings.Contains(loc, "?") {
			loc += "&digest=" + digest
		} else {
			loc += "?digest=" + digest
		}
	}
	req, _ := http.NewRequest(http.MethodPut, loc, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(data))
	r3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r3.Body.Close()
	if r3.StatusCode >= 300 {
		b, _ := io.ReadAll(r3.Body)
		t.Fatalf("blob put: %d %s", r3.StatusCode, b)
	}
}

func tarGzipLayer(t *testing.T, files map[string][]byte) (gzipBlob []byte, gzipDigest, diffID string) {
	t.Helper()
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	for name, data := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	uncompressed := tarBuf.Bytes()
	diffID = "sha256:" + sha256hex(uncompressed)

	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	if _, err := gz.Write(uncompressed); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	gzipBlob = gzBuf.Bytes()
	gzipDigest = "sha256:" + sha256hex(gzipBlob)
	return gzipBlob, gzipDigest, diffID
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func genExpiringCert(t *testing.T, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "htest-tls"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pemBlock("CERTIFICATE", der)
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pemBlock("EC PRIVATE KEY", keyBytes)
	return certPEM, keyPEM
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

func defaultVulnCheckEvery() time.Duration {
	// Production default asserted by VulnConfigFromEnv / manager.
	return 10 * time.Minute
}

var _ = fmt.Sprintf
