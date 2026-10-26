package main

import (
	"context"
	"strings"
	"testing"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
)

type fakeRestarter struct {
	detail    dockerapi.ContainerDetail
	restarted []string
}

func (f *fakeRestarter) InspectContainer(context.Context, string) (dockerapi.ContainerDetail, error) {
	return f.detail, nil
}

func (f *fakeRestarter) RestartContainer(_ context.Context, id string) error {
	f.restarted = append(f.restarted, id)
	return nil
}

func TestRestartGuard(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name, ctrName, hostname string
		wantErr                 string
	}{
		{"normal", "/web-1", "host", ""},
		{"caddy", "/haetae-caddy", "host", "ingress"},
		{"self", "/agent", id[:12], "own container"},
		{"short hostname is not an id", "/web-1", "agent", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRestarter{detail: dockerapi.ContainerDetail{ID: id, Name: tc.ctrName}}
			err := restartGuard{docker: f, caddyName: "haetae-caddy", hostname: tc.hostname}.restart(context.Background(), "x")
			if tc.wantErr == "" {
				if err != nil || len(f.restarted) != 1 || f.restarted[0] != id {
					t.Fatalf("err=%v restarted=%v", err, f.restarted)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || len(f.restarted) != 0 {
				t.Fatalf("err=%v restarted=%v", err, f.restarted)
			}
		})
	}
}
