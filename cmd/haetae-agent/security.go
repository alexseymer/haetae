package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/alexseymer/haetae/internal/agent"
	"github.com/alexseymer/haetae/internal/agent/conntrack"
	"github.com/alexseymer/haetae/internal/agent/dockerapi"
	"github.com/alexseymer/haetae/internal/agent/imagecheck"
	"github.com/alexseymer/haetae/internal/agent/secfacts"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// securityRuntime holds the M7 host-security goroutines started with the agent.
type securityRuntime struct {
	secfacts  *secfacts.Runner
	conntrack *conntrack.Listener
	images    *imagecheck.Checker
}

// startSecurity wires misconfig collection, conntrack outbound listening, and
// image candidate checks. It returns a command handler for PullImage that
// chains after the logs handler.
func startSecurity(ctx context.Context, docker *dockerapi.Client, out *agent.Outbox, stateDir string, caddyName string, httpPort, httpsPort int, log *slog.Logger) *securityRuntime {
	ct := conntrack.NewListener(docker, out, log.With("component", "conntrack"))
	sf := secfacts.NewRunner(docker, out, log.With("component", "secfacts"))
	sf.Caddy = secfacts.CaddyPorts{ContainerName: caddyName, HTTPPort: httpPort, HTTPSPort: httpsPort}
	sf.Domains = func() []string { return readIngressDomains(stateDir) }
	sf.Conntrack = ct
	// The security report carries the conntrack state; resend it on change.
	ct.OnStateChange = sf.Trigger
	img := imagecheck.New(docker, out, log.With("component", "imagecheck"))
	// HAETAE_IMAGECHECK_EVERY is test-only; ignored unless HAETAE_TEST_HOOKS=1.
	if os.Getenv("HAETAE_TEST_HOOKS") == "1" {
		if v := os.Getenv("HAETAE_IMAGECHECK_EVERY"); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				img.Interval = d
				img.Jitter = 0
				img.FirstDelay = 2 * time.Second
			}
		}
	}

	go sf.Run(ctx)
	go ct.Run(ctx)
	go img.Run(ctx)

	return &securityRuntime{secfacts: sf, conntrack: ct, images: img}
}

func (r *securityRuntime) TriggerScan() {
	if r != nil && r.secfacts != nil {
		r.secfacts.Trigger()
	}
}

func (r *securityRuntime) Handle(ctx context.Context, cmd *agentv1.Command) *agentv1.CommandResult {
	if r == nil || r.images == nil {
		return nil
	}
	return r.images.Handle(ctx, cmd)
}

func readIngressDomains(stateDir string) []string {
	path := filepath.Join(stateDir, "ingress", "routes.json")
	b, err := os.ReadFile(path) //nolint:gosec // agent-owned state file
	if err != nil {
		return nil
	}
	var routes map[string]struct {
		Domain string `json:"domain"`
	}
	if err := json.Unmarshal(b, &routes); err != nil {
		return nil
	}
	var out []string
	for _, r := range routes {
		if r.Domain != "" {
			out = append(out, r.Domain)
		}
	}
	return out
}
