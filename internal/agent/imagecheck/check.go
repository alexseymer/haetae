package imagecheck

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// Emitter sends ImageCandidates.
type Emitter interface {
	PutImageCandidates(*agentv1.ImageCandidates)
}

// Checker looks up registry digests for running containers.
type Checker struct {
	Docker   *dockerapi.Client
	Registry *RegistryClient
	Emit     Emitter
	Log      *slog.Logger
	Now      func() time.Time
	Interval time.Duration // default 24h
	Jitter   time.Duration // default 1h
	// FirstDelay is the settle time before the first Check (default 30s).
	FirstDelay time.Duration
}

// New returns a Checker.
func New(docker *dockerapi.Client, emit Emitter, log *slog.Logger) *Checker {
	if log == nil {
		log = slog.Default()
	}
	return &Checker{
		Docker:   docker,
		Registry: &RegistryClient{},
		Emit:     emit,
		Log:      log,
		Now:      time.Now,
		Interval: 24 * time.Hour,
		Jitter:   time.Hour,
	}
}

// Run emits candidates daily (with jitter) until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	first := c.FirstDelay
	if first <= 0 {
		first = 30 * time.Second
	}
	// First run after a short delay so the agent settles.
	if !sleepCtx(ctx, first) {
		return
	}
	c.Check(ctx)
	for {
		wait := c.Interval
		if c.Jitter > 0 {
			wait += rand.N(c.Jitter) //nolint:gosec
		}
		if wait <= 0 {
			wait = 24 * time.Hour
		}
		if !sleepCtx(ctx, wait) {
			return
		}
		c.Check(ctx)
	}
}

// Check looks up every running container image and emits ImageCandidates.
func (c *Checker) Check(ctx context.Context) {
	if c.Docker == nil {
		return
	}
	list, err := c.Docker.ListContainers(ctx, false)
	if err != nil {
		c.Log.Debug("imagecheck list", "err", err)
		return
	}
	out := &agentv1.ImageCandidates{}
	for _, ctr := range list {
		cand := c.one(ctx, ctr)
		if cand != nil {
			out.Candidates = append(out.Candidates, cand)
		}
	}
	if c.Emit != nil && len(out.Candidates) > 0 {
		c.Emit.PutImageCandidates(out)
	}
}

func (c *Checker) one(ctx context.Context, ctr dockerapi.ContainerSummary) *agentv1.ImageCandidate {
	ref := ctr.Image
	cand := &agentv1.ImageCandidate{
		ContainerId: ctr.ID,
		Image:       ref,
	}
	if isDigestPinned(ref) {
		cand.Status = "unknown"
		cand.Error = "digest-pinned reference cannot change"
		cand.RunningDigest = digestFromRef(ref)
		return cand
	}
	if isLocalOnly(ref) {
		cand.Status = "unknown"
		cand.Error = "local-only image"
		return cand
	}

	img, err := c.Docker.InspectImage(ctx, ref)
	if err == nil {
		cand.RunningDigest = repoDigest(img.RepoDigests, ref)
	}

	info, err := c.Registry.Lookup(ctx, ref)
	if err != nil {
		cand.Status = "unknown"
		cand.Error = clip(err.Error(), 200)
		return cand
	}
	cand.CandidateDigest = info.Digest
	if !info.CreatedAt.IsZero() {
		cand.CandidateCreatedUnixMs = info.CreatedAt.UnixMilli()
	}
	cand.Status = "ok"
	return cand
}

func isDigestPinned(ref string) bool {
	return strings.Contains(ref, "@sha256:")
}

func digestFromRef(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[i+1:]
	}
	return ""
}

func isLocalOnly(ref string) bool {
	// Image id / sha256 hex without a registry name.
	if strings.HasPrefix(ref, "sha256:") {
		return true
	}
	if len(ref) == 64 && !strings.Contains(ref, "/") && !strings.Contains(ref, ":") {
		return true
	}
	return false
}

func repoDigest(digests []string, ref string) string {
	// Prefer digest matching the repository name.
	repo := ref
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo = ref[:i]
	}
	for _, d := range digests {
		if strings.HasPrefix(d, repo+"@") {
			if i := strings.Index(d, "@"); i >= 0 {
				return d[i+1:]
			}
		}
	}
	if len(digests) > 0 {
		if i := strings.Index(digests[0], "@"); i >= 0 {
			return digests[0][i+1:]
		}
	}
	return ""
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
