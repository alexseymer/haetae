//go:build release

// Package release verifies the artifacts produced by `make release` and
// `make release-image`. Run with `make test-release`.
package release

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"archive/tar"
	"compress/gzip"
)

var arches = map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}
var programs = []string{"haetae-agent", "haetae-inventory", "haetae-server"}

func env(t *testing.T, k string) string {
	t.Helper()
	v := os.Getenv(k)
	if v == "" {
		t.Skipf("%s not set (use make test-release)", k)
	}
	return v
}

// sumLine matches the format sha256sum emits and agent-install.sh verifies.
var sumLine = regexp.MustCompile(`^[0-9a-f]{64}  [A-Za-z0-9._-]+$`)

func readSums(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sums := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if !sumLine.MatchString(sc.Text()) {
			t.Fatalf("%s: line %q is not '<sha256><2 spaces><name>'", path, sc.Text())
		}
		sums[sc.Text()[66:]] = sc.Text()[:64]
	}
	return sums
}

func fileSum(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestChecksums(t *testing.T) {
	dir, ver := env(t, "HAETAE_RELEASE_DIR"), env(t, "HAETAE_RELEASE_VERSION")
	sums := readSums(t, filepath.Join(dir, "SHA256SUMS"))
	var want []string
	for a := range arches {
		for _, p := range programs {
			want = append(want, fmt.Sprintf("%s-linux-%s", p, a))
		}
		want = append(want, fmt.Sprintf("haetae-%s-linux-%s.tar.gz", ver, a))
	}
	for _, n := range want {
		got, ok := sums[n]
		if !ok {
			t.Errorf("SHA256SUMS lacks %s", n)
			continue
		}
		if real := fileSum(t, filepath.Join(dir, n)); real != got {
			t.Errorf("%s: checksum mismatch %s != %s", n, real, got)
		}
	}
	if len(sums) != len(want) {
		t.Errorf("SHA256SUMS has %d entries, want %d", len(sums), len(want))
	}
}

func TestELFStatic(t *testing.T) {
	dir := env(t, "HAETAE_RELEASE_DIR")
	for a, machine := range arches {
		for _, p := range programs {
			path := filepath.Join(dir, fmt.Sprintf("%s-linux-%s", p, a))
			checkELF(t, path, machine)
		}
	}
}

func checkELF(t *testing.T, path string, machine elf.Machine) {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return
	}
	defer f.Close()
	if f.Machine != machine || f.Class != elf.ELFCLASS64 || f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX {
		t.Errorf("%s: machine %v class %v osabi %v, want %v", path, f.Machine, f.Class, f.OSABI, machine)
	}
	for _, pr := range f.Progs {
		if pr.Type == elf.PT_INTERP {
			t.Errorf("%s: has a dynamic interpreter (not static)", path)
		}
	}
	if libs, _ := f.ImportedLibraries(); len(libs) > 0 {
		t.Errorf("%s: NEEDED libraries %v", path, libs)
	}
	if f.Section(".interp") != nil {
		t.Errorf("%s: has .interp", path)
	}
}

func TestVersionOutput(t *testing.T) {
	dir, ver := env(t, "HAETAE_RELEASE_DIR"), env(t, "HAETAE_RELEASE_VERSION")
	_, qemuErr := exec.LookPath("qemu-aarch64-static")
	if qemuErr == nil {
		t.Log("qemu-aarch64-static present: executing arm64 binaries")
	} else {
		t.Log("arm64 binaries are NOT executed (no qemu-user here): ELF header assertions only (TestELFStatic)")
	}
	for a := range arches {
		for _, p := range programs {
			path := filepath.Join(dir, fmt.Sprintf("%s-linux-%s", p, a))
			var cmd *exec.Cmd
			switch {
			case a == "amd64":
				cmd = exec.Command(path, "--version")
			case qemuErr == nil:
				cmd = exec.Command("qemu-aarch64-static", path, "--version")
			default:
				continue
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("%s --version: %v\n%s", path, err, out)
				continue
			}
			if !strings.HasPrefix(strings.TrimSpace(string(out)), p+" "+ver+" (") {
				t.Errorf("%s --version = %q, want prefix %q", path, out, p+" "+ver+" (")
			}
		}
	}
}

func TestTarballs(t *testing.T) {
	dir, ver := env(t, "HAETAE_RELEASE_DIR"), env(t, "HAETAE_RELEASE_VERSION")
	for a := range arches {
		name := fmt.Sprintf("haetae-%s-linux-%s", ver, a)
		f, err := os.Open(filepath.Join(dir, name+".tar.gz"))
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		tr := tar.NewReader(zr)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			got[strings.TrimPrefix(h.Name, name+"/")] = true
			// each binary must equal the standalone artifact
			if base := strings.TrimPrefix(h.Name, name+"/"); strings.HasPrefix(base, "haetae-") {
				b, _ := io.ReadAll(tr)
				sum := sha256.Sum256(b)
				if hex.EncodeToString(sum[:]) != fileSum(t, filepath.Join(dir, fmt.Sprintf("%s-linux-%s", base, a))) {
					t.Errorf("%s: %s differs from standalone binary", name, base)
				}
			}
		}
		f.Close()
		for _, w := range append([]string{"LICENSE", "README.md"}, programs...) {
			if !got[w] {
				t.Errorf("%s.tar.gz lacks %s", name, w)
			}
		}
	}
}

// The installer (internal/installer) serves exactly these names with a dist SHA256SUMS and
// agent-install.sh verifies "<sum>  <name>" lines; the release binaries must equal them.
func TestInstallerExpectations(t *testing.T) {
	dir, dist := env(t, "HAETAE_RELEASE_DIR"), env(t, "HAETAE_DIST_DIR")
	sums := readSums(t, filepath.Join(dist, "SHA256SUMS"))
	rel := readSums(t, filepath.Join(dir, "SHA256SUMS"))
	names := []string{"haetae-agent-linux-amd64", "haetae-agent-linux-arm64", "haetae-inventory-linux-amd64", "haetae-inventory-linux-arm64"}
	if len(sums) != len(names) {
		t.Errorf("dist SHA256SUMS has %d entries, want %d", len(sums), len(names))
	}
	for _, n := range names {
		if sums[n] == "" {
			t.Errorf("dist SHA256SUMS lacks %s", n)
		} else if fileSum(t, filepath.Join(dist, n)) != sums[n] {
			t.Errorf("dist %s checksum mismatch", n)
		}
		if rel[n] != sums[n] {
			t.Errorf("release %s differs from dist build", n)
		}
	}
	// Run the installer's own verification command shape.
	for _, n := range names {
		cmd := exec.Command("sha256sum", "-c", "-")
		cmd.Dir = dist
		cmd.Stdin = strings.NewReader(sums[n] + "  " + n + "\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("sha256sum -c for %s: %v %s", n, err, out)
		}
	}
}

func TestServerImage(t *testing.T) {
	dir, ver := env(t, "HAETAE_RELEASE_DIR"), env(t, "HAETAE_RELEASE_VERSION")
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("docker not available")
	}
	image := "haetae-server:" + ver
	if out, err := exec.Command("docker", "image", "inspect", image).CombinedOutput(); err != nil {
		t.Fatalf("image %s missing (make release-image): %v %s", image, err, out)
	}
	out, err := exec.Command("docker", "run", "--rm", "--name", "htest-release-version", "--entrypoint", "/usr/local/bin/haetae-server", image, "--version").CombinedOutput()
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", "htest-release-version").Run() })
	if err != nil || !strings.HasPrefix(string(out), "haetae-server "+ver+" (") {
		t.Fatalf("image --version = %q err %v", out, err)
	}

	user, _ := exec.Command("docker", "inspect", "-f", "{{.Config.User}}", image).Output()
	u := strings.TrimSpace(string(user))
	if u == "" || u == "0" || u == "root" || strings.HasPrefix(u, "0:") {
		t.Errorf("image runs as %q, want non-root", u)
	}

	// The binary in the image is the release binary, and that binary embeds the built web UI.
	cid := strings.TrimSpace(mustOut(t, "docker", "create", "--name", "htest-release-cp", image))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", "htest-release-cp").Run() })
	tmp := filepath.Join(t.TempDir(), "srv")
	mustOut(t, "docker", "cp", cid+":/usr/local/bin/haetae-server", tmp)
	if fileSum(t, tmp) != fileSum(t, filepath.Join(dir, "haetae-server-linux-amd64")) {
		t.Error("image binary differs from bin/release/haetae-server-linux-amd64")
	}
	bin, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(bin, []byte("app/index.html")) || !regexp.MustCompile(`app/assets/index-[A-Za-z0-9_-]+\.js`).Match(bin) {
		t.Error("server binary does not embed the built web UI (app/index.html, app/assets/index-*.js)")
	}
	// Download dir served by the installer endpoint is present in the image.
	distTmp := t.TempDir()
	mustOut(t, "docker", "cp", cid+":/usr/share/haetae/dist/SHA256SUMS", filepath.Join(distTmp, "SHA256SUMS"))
	if len(readSums(t, filepath.Join(distTmp, "SHA256SUMS"))) != 4 {
		t.Error("image dist/SHA256SUMS does not list the four download files")
	}
}

func mustOut(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}
