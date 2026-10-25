package gateway

import (
	"context"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// Handlers for the M7 messages. Each is replaced by its owner in its own file
// (security_sbom.go, security_host.go) and removed from here; see
// docs/design/m7-security.md.

func (g *Gateway) handleSbomReport(ctx context.Context, s *session, r *agentv1.SbomReport) {}

func (g *Gateway) handleSecurityReport(ctx context.Context, s *session, r *agentv1.SecurityReport) {}

func (g *Gateway) handleOutboundBatch(ctx context.Context, s *session, b *agentv1.OutboundBatch) {}

func (g *Gateway) handleImageCandidates(ctx context.Context, s *session, c *agentv1.ImageCandidates) {
}
