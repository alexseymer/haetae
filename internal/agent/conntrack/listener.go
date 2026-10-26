package conntrack

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

const (
	defaultWindow   = 60 * time.Second
	maxDestinations = 200
	maxBackoff      = 30 * time.Second
	minBackoff      = time.Second
)

// Emitter sends OutboundBatch messages.
type Emitter interface {
	PutOutboundBatch(*agentv1.OutboundBatch)
}

// Listener maps NEW events to containers, aggregates per window, and reports state.
type Listener struct {
	Source     EventSource
	Docker     *dockerapi.Client
	Emit       Emitter
	Window     time.Duration
	MinBackoff time.Duration // default 1s; tests shrink this
	MaxBackoff time.Duration // default 30s
	Log        *slog.Logger
	Now        func() time.Time
	// DegradedHold is how long "degraded" stays reported after the last event
	// loss / error, even when the source restarted fine (default 5m).
	DegradedHold time.Duration
	// OnStateChange, if set, is called (outside locks) whenever the reported
	// state changes, so the caller can send a state sync promptly.
	OnStateChange func()

	mu            sync.Mutex
	degradedUntil time.Time
	degradedWhy   string
	state         string
	reason        string
	notMonitored  []string
	ipToContainer map[string]string // container IP → container id
	dockerNets    []netip.Prefix    // bridge/docker subnets
	hostNetIDs    map[string]bool

	agg      map[aggKey]*aggVal
	dropped  uint32
	winStart time.Time
}

type aggKey struct {
	containerID string
	dstIP       string
	dstPort     uint16
	proto       string
}

type aggVal struct {
	count uint32
}

// NewListener returns a Listener. Source defaults to KernelSource.
func NewListener(docker *dockerapi.Client, emit Emitter, log *slog.Logger) *Listener {
	if log == nil {
		log = slog.Default()
	}
	return &Listener{
		Source:        KernelSource{},
		Docker:        docker,
		Emit:          emit,
		Window:        defaultWindow,
		Log:           log,
		Now:           time.Now,
		state:         "unknown",
		ipToContainer: make(map[string]string),
		hostNetIDs:    make(map[string]bool),
		agg:           make(map[aggKey]*aggVal),
	}
}

// Status returns the current ConntrackStatus (for SecurityReport).
func (l *Listener) Status() *agentv1.ConntrackStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	state, reason := l.effectiveLocked()
	out := &agentv1.ConntrackStatus{
		State:  state,
		Reason: reason,
	}
	out.NotMonitoredContainerIds = append([]string(nil), l.notMonitored...)
	return out
}

// RefreshInventory rebuilds the IP→container map from Docker.
func (l *Listener) RefreshInventory(ctx context.Context) error {
	if l.Docker == nil {
		return nil
	}
	list, err := l.Docker.ListContainers(ctx, false)
	if err != nil {
		return err
	}
	nets, err := l.Docker.ListNetworks(ctx)
	if err != nil {
		return err
	}

	ipMap := make(map[string]string)
	hostNet := make(map[string]bool)
	var notMon []string
	var prefixes []netip.Prefix
	for _, n := range nets {
		for _, c := range n.IPAM.Config {
			if c.Subnet == "" {
				continue
			}
			if p, err := netip.ParsePrefix(c.Subnet); err == nil {
				prefixes = append(prefixes, p)
			}
		}
	}

	for _, c := range list {
		d, err := l.Docker.InspectContainer(ctx, c.ID)
		if err != nil {
			continue
		}
		mode := strings.ToLower(strings.TrimSpace(d.HostConfig.NetworkMode))
		if mode == "host" {
			hostNet[c.ID] = true
			notMon = append(notMon, c.ID)
			continue
		}
		for _, ep := range d.NetworkSettings.Networks {
			if ep.IPAddress != "" {
				ipMap[ep.IPAddress] = c.ID
			}
			if ep.GlobalIPv6Address != "" {
				ipMap[ep.GlobalIPv6Address] = c.ID
			}
		}
	}

	l.mu.Lock()
	l.ipToContainer = ipMap
	l.dockerNets = prefixes
	l.hostNetIDs = hostNet
	l.notMonitored = notMon
	l.mu.Unlock()
	return nil
}

// Run listens until ctx ends. It restarts the source with bounded exponential
// backoff on transient errors; unavailable stays unavailable.
func (l *Listener) Run(ctx context.Context) {
	if l.Source == nil {
		l.Source = KernelSource{}
	}
	_ = l.RefreshInventory(ctx)

	go l.watchDocker(ctx)

	l.mu.Lock()
	l.winStart = l.Now()
	l.mu.Unlock()

	minB, maxB := l.MinBackoff, l.MaxBackoff
	if minB <= 0 {
		minB = minBackoff
	}
	if maxB <= 0 {
		maxB = maxBackoff
	}
	backoff := minB
	for ctx.Err() == nil {
		err := l.runOnce(ctx)
		l.flush(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			backoff = minB
			continue
		}
		switch {
		case isUnavailable(err):
			// Stay unavailable across rare probes; do not flip to ok until a
			// subsequent runOnce stays up (runOnce only sets ok after settle).
			l.setState("unavailable", err.Error())
			backoff = maxB
		case isDegraded(err):
			l.setState("degraded", err.Error())
		default:
			l.setState("degraded", err.Error())
		}
		l.Log.Debug("conntrack listener restart", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if !isUnavailable(err) && backoff < maxB {
			backoff *= 2
			if backoff > maxB {
				backoff = maxB
			}
		}
	}
}

func (l *Listener) runOnce(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	window := l.Window
	if window <= 0 {
		window = defaultWindow
	}
	ticker := time.NewTicker(window)
	defer ticker.Stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- l.Source.Run(ctx, func(ev Event) { l.onEvent(ev) })
	}()

	// Mark ok only after the source has stayed up briefly so unavailable
	// probes that fail immediately do not clear the sticky state.
	settle := time.NewTimer(50 * time.Millisecond)
	defer settle.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-settle.C:
			l.setState("ok", "")
		case <-ticker.C:
			l.flush(ctx)
		case err := <-errCh:
			if err == nil && ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func (l *Listener) onEvent(ev Event) {
	if !ev.OrigSrc.IsValid() || !ev.OrigDst.IsValid() {
		return
	}
	if dropDestination(ev.OrigDst) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inDockerNetLocked(ev.OrigDst) {
		return // container-to-container
	}
	cid, ok := l.ipToContainer[ev.OrigSrc.String()]
	if !ok {
		return
	}
	if l.hostNetIDs[cid] {
		return
	}
	key := aggKey{
		containerID: cid,
		dstIP:       ev.OrigDst.String(),
		dstPort:     ev.OrigDstPort,
		proto:       ev.Proto,
	}
	if v, exists := l.agg[key]; exists {
		v.count++
		return
	}
	if len(l.agg) >= maxDestinations {
		l.dropped++
		return
	}
	l.agg[key] = &aggVal{count: 1}
}

func (l *Listener) flush(ctx context.Context) {
	l.mu.Lock()
	if len(l.agg) == 0 && l.dropped == 0 {
		l.winStart = l.Now()
		l.mu.Unlock()
		return
	}
	start := l.winStart
	end := l.Now()
	batch := &agentv1.OutboundBatch{
		WindowStartUnixMs: start.UnixMilli(),
		WindowEndUnixMs:   end.UnixMilli(),
		Dropped:           l.dropped,
	}
	for k, v := range l.agg {
		batch.Conns = append(batch.Conns, &agentv1.OutboundConn{
			ContainerId: k.containerID,
			DstIp:       k.dstIP,
			DstPort:     uint32(k.dstPort),
			Proto:       k.proto,
			Count:       v.count,
		})
	}
	l.agg = make(map[aggKey]*aggVal)
	l.dropped = 0
	l.winStart = end
	l.mu.Unlock()

	if l.Emit != nil && (len(batch.Conns) > 0 || batch.Dropped > 0) {
		l.Emit.PutOutboundBatch(batch)
	}
}

const defaultDegradedHold = 5 * time.Minute

// effectiveLocked applies the sticky degraded hold on top of the raw state.
func (l *Listener) effectiveLocked() (state, reason string) {
	if l.state == "ok" && l.Now().Before(l.degradedUntil) {
		return "degraded", l.degradedWhy
	}
	return l.state, l.reason
}

func (l *Listener) setState(state, reason string) {
	l.mu.Lock()
	oldS, oldR := l.effectiveLocked()
	if state == "degraded" {
		hold := l.DegradedHold
		if hold <= 0 {
			hold = defaultDegradedHold
		}
		l.degradedUntil = l.Now().Add(hold)
		l.degradedWhy = reason
	}
	l.state = state
	l.reason = reason
	newS, newR := l.effectiveLocked()
	cb := l.OnStateChange
	l.mu.Unlock()
	if cb != nil && (oldS != newS || oldR != newR) {
		cb()
	}
}

func (l *Listener) inDockerNetLocked(addr netip.Addr) bool {
	for _, p := range l.dockerNets {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func dropDestination(addr netip.Addr) bool {
	return addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified()
}

func isUnavailable(err error) bool {
	var u unavailableError
	return errors.As(err, &u)
}

func isDegraded(err error) bool {
	var d degradedError
	return errors.As(err, &d)
}

// refreshEvent reports whether a Docker event can change the IP→container map.
func refreshEvent(ev dockerapi.Event) bool {
	action, _, _ := strings.Cut(ev.Action, ":")
	switch ev.Type {
	case "container":
		return action == "start" || action == "die" || action == "destroy"
	case "network":
		return action == "connect" || action == "disconnect"
	}
	return false
}

// refresher runs RefreshInventory for each trigger; triggers arriving while a
// refresh is in flight collapse into one pending run.
func (l *Listener) refresher(ctx context.Context, trigger <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			_ = l.RefreshInventory(ctx)
		}
	}
}

func (l *Listener) watchDocker(ctx context.Context) {
	if l.Docker == nil {
		return
	}
	trigger := make(chan struct{}, 1) // one in flight (worker) + one pending (buffer)
	kick := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}
	go l.refresher(ctx, trigger)
	// Periodic refresh is a backstop for missed events.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				kick()
			}
		}
	}()
	backoff := time.Second
	for ctx.Err() == nil {
		err := l.Docker.EventsFor(ctx, []string{"container", "network"}, func(ev dockerapi.Event) {
			if refreshEvent(ev) {
				kick()
			}
		})
		if ctx.Err() != nil {
			return
		}
		l.Log.Debug("docker events ended", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// SetInventoryForTest installs a fixed IP map and docker subnets (unit tests).
func (l *Listener) SetInventoryForTest(ipToContainer map[string]string, dockerNets []netip.Prefix, hostNetIDs []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ipToContainer = ipToContainer
	l.dockerNets = dockerNets
	l.hostNetIDs = make(map[string]bool)
	l.notMonitored = append([]string(nil), hostNetIDs...)
	for _, id := range hostNetIDs {
		l.hostNetIDs[id] = true
	}
}
