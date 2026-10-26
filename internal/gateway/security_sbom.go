package gateway

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/vuln"
)

const (
	maxSbomPackages  = store.MaxSbomPackages
	sbomMatchWorkers = 2
	sbomReportRate   = 5 // per second, per session
	sbomReportBurst  = 20
)

// SbomStore is the persistence handleSbomReport needs.
type SbomStore interface {
	ContainerBelongsToHost(ctx context.Context, orgID, hostID uuid.UUID, containerID string) (bool, error)
	SaveSbom(ctx context.Context, orgID uuid.UUID, in store.SaveSbomInput) (store.Scan, error)
	RecordFailedScan(ctx context.Context, orgID uuid.UUID, in store.FailedScanInput) (store.Scan, error)
	ReplaceFindings(ctx context.Context, orgID uuid.UUID, imageID string, findings []store.Finding) error
	SetMatchState(ctx context.Context, orgID uuid.UUID, imageID, status, errText string, dbBuiltAt *time.Time) error
}

// MatchQueue matches an SBOM after it is stored (bounded; not on the receive loop).
type MatchQueue interface {
	// EnqueueMatch returns false when the job was not queued (buffer full).
	EnqueueMatch(orgID uuid.UUID, imageID string) bool
	Ready() bool
}

// SetSbomHandlers wires SBOM persistence and optional matcher queue onto the gateway.
func (g *Gateway) SetSbomHandlers(st SbomStore, q MatchQueue) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sbomStore = st
	g.matchQueue = q
}

func (g *Gateway) handleSbomReport(ctx context.Context, s *session, r *agentv1.SbomReport) {
	if r == nil {
		return
	}
	g.mu.Lock()
	st := g.sbomStore
	q := g.matchQueue
	g.mu.Unlock()
	if st == nil {
		// Fall back to the full store when not explicitly wired.
		if ss, ok := g.store.(SbomStore); ok {
			st = ss
		}
	}
	if st == nil {
		g.log.Error("sbom report: no store")
		return
	}

	if s.sbomLim != nil && !s.sbomLim.Allow() {
		s.droppedSecurity.Add(1)
		return
	}

	status := strings.ToLower(strings.TrimSpace(r.GetStatus()))
	switch status {
	case store.ScanOK, store.ScanFailed, store.ScanTimeout, store.ScanOOM, store.ScanUnsupported:
	default:
		g.log.Warn("sbom report: bad status", "host_id", s.hostID, "status", r.GetStatus())
		return
	}
	trigger := strings.ToLower(strings.TrimSpace(r.GetTrigger()))
	switch trigger {
	case "start", "deploy", "daily", "forced", "candidate":
	default:
		g.log.Warn("sbom report: bad trigger", "host_id", s.hostID, "trigger", r.GetTrigger())
		return
	}
	cid := clip(r.GetContainerId())
	if cid != "" {
		ok, err := st.ContainerBelongsToHost(ctx, s.orgID, s.hostID, cid)
		if err != nil {
			g.log.Error("sbom report: container check", "err", err)
			return
		}
		if !ok {
			g.log.Warn("sbom report: foreign container", "host_id", s.hostID, "container_id", cid)
			return
		}
	}

	if len(r.GetPackages()) > maxSbomPackages {
		g.log.Warn("sbom report: too many packages", "host_id", s.hostID, "n", len(r.GetPackages()))
		_, _ = st.RecordFailedScan(ctx, s.orgID, store.FailedScanInput{
			HostID: s.hostID, ContainerID: clip(r.GetContainerId()), Image: clip(r.GetImage()),
			ImageID: clip(r.GetImageId()), Trigger: trigger, Status: store.ScanFailed,
			Error: "package count exceeds 20000", ToolVersion: clip(r.GetToolVersion()),
			PeakMemoryBytes: r.GetPeakMemoryBytes(), SourceRef: clip(r.GetSourceRef()),
			StartedAt: unixMs(r.GetStartedUnixMs()), FinishedAt: unixMs(r.GetFinishedUnixMs()),
		})
		return
	}

	if status != store.ScanOK {
		errText := clip(r.GetError())
		if errText == "" {
			errText = "scan failed"
		}
		_, err := st.RecordFailedScan(ctx, s.orgID, store.FailedScanInput{
			HostID: s.hostID, ContainerID: cid, Image: clip(r.GetImage()),
			ImageID: clip(r.GetImageId()), Trigger: trigger, Status: status,
			Error: errText, ToolVersion: clip(r.GetToolVersion()),
			PeakMemoryBytes: r.GetPeakMemoryBytes(), SourceRef: clip(r.GetSourceRef()),
			StartedAt: unixMs(r.GetStartedUnixMs()), FinishedAt: unixMs(r.GetFinishedUnixMs()),
		})
		if err != nil && !errors.Is(err, store.ErrForeignContainer) {
			g.log.Error("sbom report: record failed", "err", err)
		}
		return
	}

	pkgs := make([]store.SbomPackage, 0, len(r.GetPackages()))
	for _, p := range r.GetPackages() {
		pkgs = append(pkgs, store.SbomPackage{
			Name: clip(p.GetName()), Version: clip(p.GetVersion()),
			Type: clip(p.GetType()), PURL: clip(p.GetPurl()),
		})
	}
	_, err := st.SaveSbom(ctx, s.orgID, store.SaveSbomInput{
		HostID: s.hostID, ContainerID: cid, Image: clip(r.GetImage()),
		ImageID: clip(r.GetImageId()), Trigger: trigger, ToolVersion: clip(r.GetToolVersion()),
		PeakMemoryBytes: r.GetPeakMemoryBytes(), SourceRef: clip(r.GetSourceRef()),
		StartedAt: unixMs(r.GetStartedUnixMs()), FinishedAt: unixMs(r.GetFinishedUnixMs()),
		DistroName: clip(r.GetDistroName()), DistroVersion: clip(r.GetDistroVersion()),
		DistroIDLike: clip(r.GetDistroIdLike()), Packages: pkgs,
	})
	if err != nil {
		if errors.Is(err, store.ErrForeignContainer) || errors.Is(err, store.ErrInvalidSbom) {
			g.log.Warn("sbom report rejected", "err", err)
			return
		}
		g.log.Error("sbom report: save", "err", err)
		return
	}

	imageID := clip(r.GetImageId())
	if imageID == "" || q == nil {
		return
	}
	// Never lose a match silently: when the database is not ready or the queue
	// is full, mark the image pending; the manager's periodic re-match picks up
	// pending and missing images.
	if !q.Ready() || !q.EnqueueMatch(s.orgID, imageID) {
		if err := st.SetMatchState(ctx, s.orgID, imageID, store.MatchPending, "", nil); err != nil {
			g.log.Error("sbom report: mark match pending", "err", err)
		}
	}
}

// MatchWorker is a bounded queue that matches SBOMs off the receive path.
type MatchWorker struct {
	mgr    *vuln.Manager
	log    *slog.Logger
	jobs   chan matchJob
	cancel context.CancelFunc
}

type matchJob struct {
	orgID   uuid.UUID
	imageID string
}

// NewMatchWorker starts workers that call mgr.MatchImage.
func NewMatchWorker(parent context.Context, mgr *vuln.Manager, log *slog.Logger, workers int) *MatchWorker {
	if workers <= 0 {
		workers = sbomMatchWorkers
	}
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(parent)
	w := &MatchWorker{
		mgr:    mgr,
		log:    log,
		jobs:   make(chan matchJob, 64),
		cancel: cancel,
	}
	for i := 0; i < workers; i++ {
		go w.loop(ctx)
	}
	return w
}

func (w *MatchWorker) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-w.jobs:
			if err := w.mgr.MatchImage(ctx, j.orgID, j.imageID); err != nil {
				w.log.Error("sbom match", "org", j.orgID, "image", j.imageID, "err", err)
			}
		}
	}
}

// EnqueueMatch queues a match and reports whether it was queued. A full
// buffer returns false; the caller marks the image pending for re-match.
func (w *MatchWorker) EnqueueMatch(orgID uuid.UUID, imageID string) bool {
	if w == nil || imageID == "" {
		return false
	}
	select {
	case w.jobs <- matchJob{orgID: orgID, imageID: imageID}:
		return true
	default:
		return false
	}
}

// Ready reports whether the vuln manager has a loaded database.
func (w *MatchWorker) Ready() bool {
	return w != nil && w.mgr != nil && w.mgr.Ready()
}

// Stop cancels workers.
func (w *MatchWorker) Stop() {
	if w != nil && w.cancel != nil {
		w.cancel()
	}
}
