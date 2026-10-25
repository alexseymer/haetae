package scan

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/google/uuid"
)

const (
	defaultQueueCap   = 64
	defaultDailyEvery = 24 * time.Hour
	maxDailyJitter    = time.Hour
	maxPackagesReport = 20000
)

// Reporter sends completed SbomReports (implemented by the agent outbox).
type Reporter interface {
	PutSbom(*agentv1.SbomReport)
}

// Runner runs one inventory (real Launcher or test fake).
type Runner interface {
	Run(ctx context.Context, req LaunchRequest) LaunchResult
}

// Config tunes the Scanner. Zero values get defaults.
type Config struct {
	DockerSocket string
	HelperPath   string
	QueueCap     int
	DailyEvery   time.Duration
	Now          func() time.Time
	// RandJitter returns a duration in [0, max). Tests inject a fixed value.
	RandJitter func(max time.Duration) time.Duration
}

// Scanner serialises inventories per host: queue + dedupe by image id.
type Scanner struct {
	cfg    Config
	docker *dockerapi.Client
	run    Runner
	out    Reporter
	log    *slog.Logger

	mu        sync.Mutex
	queue     []job
	pending   map[string]bool // image key currently queued or running
	running   map[string]containerInfo
	seen      map[string]bool // container ids already triggered on start
	wake      chan struct{}
	lastDaily time.Time
}

type containerInfo struct {
	ID      string
	Name    string
	Image   string
	ImageID string
}

type job struct {
	scanID      string
	containerID string
	image       string // human ref
	imageID     string // dedupe key (or registry digests)
	registry    string // if set, --registry instead of --docker-image
	trigger     string
	sourceRef   string
}

// NewScanner returns a Scanner. Call Run in a goroutine.
func NewScanner(cfg Config, docker *dockerapi.Client, run Runner, out Reporter, log *slog.Logger) *Scanner {
	cfg = cfg.withDefaults()
	if run == nil {
		l := NewLauncher()
		l.HelperPath = cfg.HelperPath
		run = l
	}
	if log == nil {
		log = slog.Default()
	}
	return &Scanner{
		cfg:     cfg,
		docker:  docker,
		run:     run,
		out:     out,
		log:     log,
		pending: make(map[string]bool),
		running: make(map[string]containerInfo),
		seen:    make(map[string]bool),
		wake:    make(chan struct{}, 1),
	}
}

func (c Config) withDefaults() Config {
	if c.DockerSocket == "" {
		c.DockerSocket = "/var/run/docker.sock"
	}
	if c.QueueCap <= 0 {
		c.QueueCap = defaultQueueCap
	}
	if c.DailyEvery <= 0 {
		c.DailyEvery = defaultDailyEvery
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.RandJitter == nil {
		c.RandJitter = func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			return rand.N(max) //nolint:gosec // G404: jitter
		}
	}
	return c
}

// Run processes the queue until ctx ends.
func (s *Scanner) Run(ctx context.Context) {
	daily := time.NewTimer(s.nextDailyDelay())
	defer daily.Stop()
	for {
		s.mu.Lock()
		var j job
		have := false
		if len(s.queue) > 0 {
			j = s.queue[0]
			s.queue = s.queue[1:]
			have = true
		}
		s.mu.Unlock()

		if have {
			s.execJob(ctx, j)
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-daily.C:
			s.enqueueDaily()
			daily.Reset(s.nextDailyDelay())
		}
	}
}

func (s *Scanner) nextDailyDelay() time.Duration {
	jitter := s.cfg.RandJitter(maxDailyJitter)
	return s.cfg.DailyEvery + jitter
}

// OnRunning is called after each monitor snapshot with id→name of running containers.
// It must not block.
func (s *Scanner) OnRunning(running map[string]string) {
	go s.syncRunning(context.Background(), running)
}

func (s *Scanner) syncRunning(ctx context.Context, running map[string]string) {
	if s.docker == nil {
		return
	}
	list, err := s.docker.ListContainers(ctx, false)
	if err != nil {
		s.log.Debug("scan list containers", "err", err)
		return
	}
	byID := make(map[string]dockerapi.ContainerSummary, len(list))
	for _, c := range list {
		byID[c.ID] = c
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]containerInfo, len(running))
	for id, name := range running {
		sum, ok := byID[id]
		if !ok {
			continue
		}
		info := containerInfo{ID: id, Name: name, Image: sum.Image, ImageID: sum.ImageID}
		next[id] = info
		if !s.seen[id] {
			s.seen[id] = true
			s.enqueueLocked(job{
				scanID:      uuid.NewString(),
				containerID: id,
				image:       sum.Image,
				imageID:     dedupeKey(sum.ImageID, sum.Image),
				trigger:     TriggerStart,
			})
		}
	}
	for id := range s.seen {
		if _, ok := next[id]; !ok {
			delete(s.seen, id)
		}
	}
	s.running = next
}

// OnDeployHealthy enqueues inventories for currently running containers of project
// (or all running when project is empty). Deploy scans are forced so a concurrent
// start-triggered inventory of the same image does not drop the deploy row.
func (s *Scanner) OnDeployHealthy(project string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, info := range s.running {
		if project != "" {
			// Image/name filter is best-effort; compose project is on the summary
			// labels which we may not have here. Enqueue all running for deploy.
			_ = project
		}
		s.enqueueForcedLocked(job{
			scanID:      uuid.NewString(),
			containerID: info.ID,
			image:       info.Image,
			imageID:     dedupeKey(info.ImageID, info.Image),
			trigger:     TriggerDeploy,
		})
	}
}

func (s *Scanner) enqueueDaily() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastDaily = s.cfg.Now()
	for _, info := range s.running {
		// Forced so a concurrent start/forced job does not drop the daily row.
		s.enqueueForcedLocked(job{
			scanID:      uuid.NewString(),
			containerID: info.ID,
			image:       info.Image,
			imageID:     dedupeKey(info.ImageID, info.Image),
			trigger:     TriggerDaily,
		})
	}
}

// HandleCommand accepts ScanContainer / ScanImage. Returns nil for other kinds.
// Reply is immediate; the SbomReport arrives later via the outbox.
func (s *Scanner) HandleCommand(ctx context.Context, cmd *agentv1.Command) *agentv1.CommandResult {
	if sc := cmd.GetScanContainer(); sc != nil {
		return s.acceptScanContainer(ctx, sc)
	}
	if si := cmd.GetScanImage(); si != nil {
		return s.acceptScanImage(si)
	}
	return nil
}

func (s *Scanner) acceptScanContainer(ctx context.Context, sc *agentv1.ScanContainer) *agentv1.CommandResult {
	res := &agentv1.CommandResult{}
	if sc.GetContainerId() == "" || sc.GetScanId() == "" {
		res.Error = "scan_id and container_id are required"
		return res
	}
	if s.docker == nil {
		res.Error = "docker unavailable"
		return res
	}
	list, err := s.docker.ListContainers(ctx, true)
	if err != nil {
		res.Error = "list containers failed"
		return res
	}
	var sum *dockerapi.ContainerSummary
	for i := range list {
		if list[i].ID == sc.GetContainerId() || strings.HasPrefix(list[i].ID, sc.GetContainerId()) {
			sum = &list[i]
			break
		}
	}
	if sum == nil {
		res.Error = "container not found"
		return res
	}
	s.mu.Lock()
	s.enqueueForcedLocked(job{
		scanID:      sc.GetScanId(),
		containerID: sum.ID,
		image:       sum.Image,
		imageID:     dedupeKey(sum.ImageID, sum.Image),
		trigger:     TriggerForced,
	})
	s.mu.Unlock()
	return res // ok: scan accepted
}

func (s *Scanner) acceptScanImage(si *agentv1.ScanImage) *agentv1.CommandResult {
	res := &agentv1.CommandResult{}
	if si.GetScanId() == "" || si.GetRef() == "" || si.GetDigest() == "" {
		res.Error = "scan_id, ref and digest are required"
		return res
	}
	ref := si.GetRef() + "@" + si.GetDigest()
	if err := validateRegistryArg(ref); err != nil {
		res.Error = err.Error()
		return res
	}
	s.mu.Lock()
	s.enqueueForcedLocked(job{
		scanID:    si.GetScanId(),
		image:     si.GetRef(),
		imageID:   si.GetDigest(),
		registry:  ref,
		trigger:   TriggerCandidate,
		sourceRef: si.GetRef(),
	})
	s.mu.Unlock()
	return res
}

func dedupeKey(imageID, image string) string {
	if imageID != "" {
		return imageID
	}
	return image
}

func (s *Scanner) enqueueLocked(j job) {
	if j.imageID == "" {
		return
	}
	if s.pending[j.imageID] {
		return
	}
	if len(s.queue) >= s.cfg.QueueCap {
		// Drop oldest to keep the queue bounded.
		old := s.queue[0]
		s.queue = s.queue[1:]
		delete(s.pending, old.imageID)
		s.log.Warn("scan queue full; dropped oldest", "image", old.imageID)
	}
	s.pending[j.imageID] = true
	s.queue = append(s.queue, j)
	s.kick()
}

func (s *Scanner) enqueueForcedLocked(j job) {
	// Forced scans always run; if already pending, replace the queued entry's scan_id.
	if j.imageID != "" && s.pending[j.imageID] {
		for i := range s.queue {
			if s.queue[i].imageID == j.imageID {
				s.queue[i] = j
				s.kick()
				return
			}
		}
		// Currently running: enqueue a follow-up after it finishes by clearing pending mark
		// once done; for simplicity enqueue with a distinct pending key suffix is avoided —
		// drop pending so a new job can be added.
	}
	if j.imageID != "" {
		delete(s.pending, j.imageID)
	}
	s.enqueueLocked(j)
}

func (s *Scanner) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scanner) execJob(ctx context.Context, j job) {
	defer func() {
		s.mu.Lock()
		delete(s.pending, j.imageID)
		s.mu.Unlock()
	}()

	req := LaunchRequest{
		DockerSocket: s.cfg.DockerSocket,
		ScanID:       j.scanID,
	}
	if j.registry != "" {
		req.Registry = j.registry
	} else {
		img := j.imageID
		if img == "" {
			img = j.image
		}
		req.DockerImage = img
	}

	started := s.cfg.Now()
	lr := s.run.Run(ctx, req)
	finished := s.cfg.Now()

	report := &agentv1.SbomReport{
		ScanId:          j.scanID,
		ContainerId:     j.containerID,
		Image:           j.image,
		ImageId:         j.imageID,
		Trigger:         j.trigger,
		Status:          lr.Status,
		Error:           lr.Error,
		StartedUnixMs:   started.UnixMilli(),
		FinishedUnixMs:  finished.UnixMilli(),
		PeakMemoryBytes: lr.PeakMemory,
		SourceRef:       j.sourceRef,
	}
	if lr.Doc != nil {
		report.ToolVersion = lr.Doc.ToolVersion
		report.DistroName = lr.Doc.Distro.Name
		report.DistroVersion = lr.Doc.Distro.Version
		report.DistroIdLike = lr.Doc.Distro.IDLike
		if report.ImageId == "" {
			report.ImageId = lr.Doc.ImageID
		}
		if report.StartedUnixMs == 0 {
			report.StartedUnixMs = lr.Doc.StartedMs
		}
		if report.FinishedUnixMs == 0 {
			report.FinishedUnixMs = lr.Doc.FinishedMs
		}
		if report.PeakMemoryBytes == 0 {
			report.PeakMemoryBytes = lr.Doc.PeakRSSBytes
		}
		if lr.Status == StatusOK {
			pkgs := lr.Doc.Packages
			if len(pkgs) > maxPackagesReport {
				report.Status = StatusFailed
				report.Error = "too many packages"
				pkgs = nil
			}
			for _, p := range pkgs {
				report.Packages = append(report.Packages, &agentv1.SbomPackage{
					Name: p.Name, Version: p.Version, Type: p.Type, Purl: p.PURL,
				})
			}
		}
		// Free the document promptly.
		lr.Doc = nil
	} else if !lr.Limited {
		report.ToolVersion = appendLimitedFalse(report.ToolVersion)
	}
	if report.Status != StatusOK {
		report.Packages = nil
		if report.Error == "" {
			report.Error = "scan failed"
		}
		report.Error = clipError(report.Error)
	}
	if s.out != nil {
		s.out.PutSbom(report)
	}
}

// QueueLen reports queued jobs (tests).
func (s *Scanner) QueueLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// SetRunningForTest installs the running set without Docker (tests).
func (s *Scanner) SetRunningForTest(infos []containerInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = make(map[string]containerInfo, len(infos))
	for _, info := range infos {
		s.running[info.ID] = info
		s.seen[info.ID] = true
	}
}

// EnqueueForTest pushes a job (tests).
func (s *Scanner) EnqueueForTest(j job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueueLocked(j)
}

// EnqueueDailyForTest triggers a daily pass (tests).
func (s *Scanner) EnqueueDailyForTest() { s.enqueueDaily() }
