package main

import (
	"strings"
	"testing"
)

func TestParseEnrollArgsTokenFlagWinsOverEnv(t *testing.T) {
	env := map[string]string{"HAETAE_ENROLL_TOKEN": "from-env"}
	got, err := parseEnrollArgs([]string{
		"--server", "https://gw.example:8443",
		"--token", "from-flag",
		"--ca-pin", "sha256:abc",
	}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "from-flag" {
		t.Fatalf("token = %q, want from-flag", got.Token)
	}
}

func TestParseEnrollArgsUsesEnvWhenFlagMissing(t *testing.T) {
	env := map[string]string{"HAETAE_ENROLL_TOKEN": "from-env"}
	got, err := parseEnrollArgs([]string{
		"--server", "https://gw.example:8443",
		"--ca-pin", "sha256:abc",
		"--state-dir", "/tmp/state",
	}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "from-env" {
		t.Fatalf("token = %q, want from-env", got.Token)
	}
	if got.StateDir != "/tmp/state" {
		t.Fatalf("state-dir = %q", got.StateDir)
	}
}

func TestParseEnrollArgsRequiresToken(t *testing.T) {
	_, err := parseEnrollArgs([]string{
		"--server", "https://gw.example:8443",
		"--ca-pin", "sha256:abc",
	}, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected error when token missing from flag and env")
	}
	if !strings.Contains(err.Error(), "HAETAE_ENROLL_TOKEN") {
		t.Fatalf("error = %v, want mention of HAETAE_ENROLL_TOKEN", err)
	}
}
