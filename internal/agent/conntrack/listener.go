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

	mu            sync.Mutex
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
	out := &agentv1.ConntrackStatus{
		State:  l.state,
		Reason: l.reason,
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

func (l *Listener) setState(state, reason string) {
	l.mu.Lock()
	l.state = state
	l.reason = reason
	l.mu.Unlock()
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

func (l *Listener) watchDocker(ctx context.Context) {
	if l.Docker == nil {
		return
	}
	// Periodic refresh covers network changes; Docker Events here is
	// container-typed only (shared client), so poll as a backstop.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = l.RefreshInventory(ctx)
			}
		}
	}()
	backoff := time.Second
	for ctx.Err() == nil {
		err := l.Docker.Events(ctx, func(ev dockerapi.Event) {
			switch {
			case ev.Type == "container", ev.Type == "network",
				strings.Contains(ev.Action, "start"), strings.Contains(ev.Action, "die"),
				strings.Contains(ev.Action, "connect"), strings.Contains(ev.Action, "disconnect"):
				_ = l.RefreshInventory(ctx)
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
