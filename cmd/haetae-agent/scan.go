package main

import (
	"context"

	"github.com/alexseymer/haetae/internal/agent"
	"github.com/alexseymer/haetae/internal/agent/deploy"
	"github.com/alexseymer/haetae/internal/agent/scan"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// deployEmitHook wraps the deploy emitter to trigger inventory on healthy.
type deployEmitHook struct {
	inner     deploy.Emitter
	onHealthy func(project string)
}

func (h *deployEmitHook) Event(e *agentv1.DeployEvent) {
	h.inner.Event(e)
	if e != nil && e.GetPhase() == agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY && h.onHealthy != nil {
		// DeployEvent has no project field; empty scans all running (inventory worker).
		h.onHealthy("")
	}
}

func (h *deployEmitHook) Log(l *agentv1.DeployLog) { h.inner.Log(l) }

// commandHandler chains scan, security (PullImage), then log command handlers.
type commandHandler struct {
	logs *agent.LogManager
	scan *scan.Scanner
	sec  *securityRuntime
}

func (h commandHandler) Handle(ctx context.Context, cmd *agentv1.Command) *agentv1.CommandResult {
	if h.scan != nil {
		if r := h.scan.HandleCommand(ctx, cmd); r != nil {
			return r
		}
	}
	if h.sec != nil {
		if r := h.sec.Handle(ctx, cmd); r != nil {
			return r
		}
	}
	if h.logs != nil {
		return h.logs.Handle(ctx, cmd)
	}
	return nil
}
