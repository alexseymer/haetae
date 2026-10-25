package main

import (
	"context"

	"github.com/alexseymer/haetae/internal/agent/deploy"
	"github.com/alexseymer/haetae/internal/agent/ingress"
)

// ingressConfig is the subset of agent run flags needed to build an Ingress.
type ingressConfig struct {
	StateDir    string
	HTTPPort    int
	HTTPSPort   int
	Runner      deploy.Runner
	TLSInternal bool
}

// newIngress returns the host ingress used for deploy domains.
func newIngress(cfg ingressConfig) (deploy.Ingress, error) {
	return ingress.New(ingress.Config{
		StateDir:    cfg.StateDir,
		HTTPPort:    cfg.HTTPPort,
		HTTPSPort:   cfg.HTTPSPort,
		TLSInternal: cfg.TLSInternal,
		Runner:      ingressRunner{r: cfg.Runner},
	})
}

// ingressRunner adapts deploy.Runner to ingress.Runner.
type ingressRunner struct{ r deploy.Runner }

func (a ingressRunner) Run(ctx context.Context, name string, args []string, onLine func(string)) error {
	return a.r.Run(ctx, deploy.Cmd{
		Name:   name,
		Args:   args,
		Stdout: onLine,
		Stderr: onLine,
	})
}
