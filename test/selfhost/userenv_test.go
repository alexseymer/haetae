//go:build selfhost

package selfhost

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const anchoreListing = "https://grype.anchore.io/databases/v6/latest.json"

func TestComposeDefaultsAndUserEnvFile(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "deploy", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	c := string(b)
	for _, want := range []string{
		"HAETAE_VULN_DB_URL: ${HAETAE_VULN_DB_URL:-" + anchoreListing + "}",
		"env_file:\n      - haetae.env\n",
		"HAETAE_DATABASE_URL:", "HAETAE_WEB_ADDR:", "HAETAE_GATEWAY_ADDR:", "HAETAE_WEB_URL:", "HAETAE_GATEWAY_URL:", "HAETAE_GATEWAY_NAMES:",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("compose.yaml lacks %q", want)
		}
	}
}

// fakeDocker returns a PATH directory whose docker answers every compose call
// successfully and reports the server healthy, and logs its arguments.
func fakeDocker(t *testing.T) (pathDir, logFile string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "docker.log")
	sh := `#!/bin/sh
printf '%s\n' "$*" >>"` + logFile + `"
case "$*" in
*"ps -q server"*) echo cid ;;
inspect*) echo healthy ;;
*"exec -T server"*) echo ABCD-1234 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(sh), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, logFile
}

func runInstallWithFakeDocker(t *testing.T, root, dir, pathDir string) string {
	t.Helper()
	cmd := exec.Command("sh", filepath.Join(root, "deploy", "install-server.sh"))
	cmd.Env = append(os.Environ(),
		"PATH="+pathDir+":"+os.Getenv("PATH"),
		"HAETAE_DIR="+dir, "HAETAE_IMAGE=example/haetae:dev", "HAETAE_NO_PULL=1",
		"HAETAE_HOST=127.0.0.1", "HAETAE_WEB_PORT=18080", "HAETAE_GATEWAY_PORT=18443",
		"COMPOSE_PROJECT_NAME=haetae-fake",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	return string(out)
}

func TestInstallCreatesAndKeepsUserEnvFile(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	pathDir, logFile := fakeDocker(t)

	out := runInstallWithFakeDocker(t, root, dir, pathDir)
	if strings.Count(out, "haetae.env") < 2 || !strings.Contains(out, "created") {
		t.Errorf("expected a line announcing haetae.env:\n%s", out)
	}
	p := filepath.Join(dir, "haetae.env")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("haetae.env missing: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	tpl, _ := os.ReadFile(p)
	for _, name := range []string{
		"HAETAE_TELEGRAM_TOKEN", "HAETAE_SLACK_BOT_TOKEN", "HAETAE_SLACK_APP_TOKEN", "HAETAE_SMTP_ADDR", "HAETAE_SMTP_FROM",
		"HAETAE_SMTP_USER", "HAETAE_SMTP_PASS", "HAETAE_SIGNAL_URL", "HAETAE_AI_PROVIDER", "HAETAE_AI_BASE_URL", "HAETAE_AI_MODEL",
		"HAETAE_AI_API_KEY", "HAETAE_WEB_TLS_CERT", "HAETAE_WEB_TLS_KEY", "HAETAE_TRUSTED_PROXIES", "HAETAE_VULN_DB_URL", "HAETAE_GATEWAY_NAMES",
	} {
		if !strings.Contains(string(tpl), "# "+name+"=") {
			t.Errorf("template lacks commented %s", name)
		}
	}
	for _, line := range strings.Split(string(tpl), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			t.Errorf("template has an active line: %q", line)
		}
	}
	compose, _ := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if !strings.Contains(string(compose), "- haetae.env") {
		t.Error("compose.yaml does not reference haetae.env")
	}
	if log, _ := os.ReadFile(logFile); !strings.Contains(string(log), "--env-file "+dir+"/haetae.env") {
		t.Errorf("compose was not given haetae.env for interpolation:\n%s", log)
	}

	// A user edit survives a re-run; mode is restored to 0600; no new notice.
	custom := "HAETAE_TELEGRAM_TOKEN=secret-token\nHAETAE_VULN_DB_URL=off\n"
	if err := os.WriteFile(p, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	out = runInstallWithFakeDocker(t, root, dir, pathDir)
	got, _ := os.ReadFile(p)
	if string(got) != custom {
		t.Errorf("haetae.env was overwritten:\n%s", got)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode after rerun = %v", st.Mode().Perm())
	}
	if strings.Contains(out, "created") {
		t.Errorf("re-run claims to have created haetae.env:\n%s", out)
	}
	if strings.Contains(out, "secret-token") {
		t.Error("installer printed a secret")
	}
}
