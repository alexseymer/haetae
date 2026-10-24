package api

import (
	"strings"
	"testing"
)

func TestInstallCommandCarriesPinsAndQuotesEverything(t *testing.T) {
	a := New(Config{WebURL: "https://haetae.example.com:8080/", GatewayURL: "https://haetae.example.com:8443", CAPin: "sha256:abc", WebPin: "sha256//Zm9v"})
	cmd := a.installCommand("T-abc123")
	for _, want := range []string{
		"curl -fsS -k --pinnedpubkey 'sha256//Zm9v'",
		"'https://haetae.example.com:8080/install.sh'",
		"| sh -s --",
		"--server 'https://haetae.example.com:8443'",
		"--token 'T-abc123'",
		"--ca-pin 'sha256:abc'",
		"--web-pin 'sha256//Zm9v'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command lacks %q:\n%s", want, cmd)
		}
	}

	// Without a pin (publicly trusted certificate) curl verifies normally: no -k.
	plain := New(Config{WebURL: "https://haetae.example.com", GatewayURL: "https://g:8443", CAPin: "sha256:abc"}).installCommand("T-x")
	if strings.Contains(plain, " -k") || strings.Contains(plain, "pinnedpubkey") {
		t.Errorf("a command for a trusted certificate must not disable verification: %s", plain)
	}

	// Hostile values cannot break out of the quoting.
	evil := New(Config{WebURL: "https://h", GatewayURL: "https://g'; rm -rf /; '", CAPin: "x"}).installCommand("T'$(id)")
	if strings.Contains(evil, "'; rm") && !strings.Contains(evil, `'"'"'`) {
		t.Errorf("quote escaping missing: %s", evil)
	}
	if !strings.Contains(evil, `'T'"'"'$(id)'`) {
		t.Errorf("token not safely quoted: %s", evil)
	}
}
