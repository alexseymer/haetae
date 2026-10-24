package agent

import (
	"testing"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

func TestExecuteRefusesUnknownCommands(t *testing.T) {
	ping := execute(&agentv1.Command{Id: "1", Kind: &agentv1.Command_Ping{Ping: &agentv1.Ping{}}})
	if ping.GetError() != "" || ping.GetCommandId() != "1" {
		t.Fatalf("ping result = %v", ping)
	}
	unknown := execute(&agentv1.Command{Id: "2"})
	if unknown.GetError() != "unsupported command" || unknown.GetCommandId() != "2" {
		t.Fatalf("unknown command result = %v", unknown)
	}
}

func TestServerName(t *testing.T) {
	for _, tc := range []struct {
		url, want string
		ok        bool
	}{
		{"https://haetae.example.com:8443", "haetae.example.com", true},
		{"https://10.0.0.5:8443", "10.0.0.5", true},
		{"https://[::1]:8443", "::1", true},
		{"http://haetae.example.com", "", false},
		{"haetae.example.com:8443", "", false},
	} {
		got, err := serverName(tc.url)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("serverName(%q) = %q, %v; want %q, ok=%v", tc.url, got, err, tc.want, tc.ok)
		}
	}
}
