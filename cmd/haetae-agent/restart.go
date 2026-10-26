package main

import (
	"context"
	"errors"
	"strings"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
)

// restarter is the part of the Docker client restartContainer needs.
type restarter interface {
	InspectContainer(ctx context.Context, id string) (dockerapi.ContainerDetail, error)
	RestartContainer(ctx context.Context, id string) error
}

// restartGuard restarts containers except the ones the agent depends on: the
// Caddy ingress container it manages and, when it runs in a container, its own
// (a container's hostname is the short id by default).
type restartGuard struct {
	docker    restarter
	caddyName string
	hostname  string
}

func (g restartGuard) restart(ctx context.Context, ref string) error {
	d, err := g.docker.InspectContainer(ctx, ref)
	if err != nil {
		return err
	}
	name := strings.TrimPrefix(d.Name, "/")
	if g.caddyName != "" && name == g.caddyName {
		return errors.New("refused: the ingress container is managed by the agent")
	}
	if g.hostname != "" && len(g.hostname) >= 12 && strings.HasPrefix(d.ID, g.hostname) {
		return errors.New("refused: the agent's own container")
	}
	// Restart by the resolved id so a name cannot change meaning between
	// the check and the call.
	return g.docker.RestartContainer(ctx, d.ID)
}
