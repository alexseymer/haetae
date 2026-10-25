package secfacts

import (
	"context"
	"log/slog"
	"time"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// Emitter sends a full-replacement SecurityReport.
type Emitter interface {
	PutSecurityReport(*agentv1.SecurityReport)
}

// ConntrackProvider supplies the latest conntrack status for reports.
type ConntrackProvider interface {
	Status() *agentv1.ConntrackStatus
}

// DomainsFunc returns managed ingress domains to TLS-probe.
type DomainsFunc func() []string

// Runner collects misconfigs on each Trigger (debounced) and emits a report.
type Runner struct {
	Docker    *dockerapi.Client
	Emit      Emitter
	Caddy     CaddyPorts
	Domains   DomainsFunc
	Conntrack ConntrackProvider
	Debounce  time.Duration
	Log       *slog.Logger

	trigger chan struct{}
}

// NewRunner returns a runner. Debounce defaults to 2s.
func NewRunner(docker *dockerapi.Client, emit Emitter, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		Docker:   docker,
		Emit:     emit,
		Debounce: 2 * time.Second,
		Log:      log,
		trigger:  make(chan struct{}, 1),
	}
}

// Trigger asks for a (debounced) security scan, e.g. after a state sync.
func (r *Runner) Trigger() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// Run collects until ctx ends.
func (r *Runner) Run(ctx context.Context) {
	debounce := r.Debounce
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	var timer *time.Timer
	for {
		var timerC <-chan time.Time
		if timer != nil {
			timerC = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-r.trigger:
			if timer == nil {
				timer = time.NewTimer(debounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(debounce)
			}
		case <-timerC:
			timer = nil
			r.scan(ctx)
		}
	}
}

func (r *Runner) scan(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	opts := Options{Caddy: r.Caddy}
	if r.Domains != nil {
		opts.Domains = r.Domains()
	}
	ms, err := Collect(ctx, r.Docker, opts)
	if err != nil {
		r.Log.Debug("secfacts collect failed", "err", err)
		return
	}
	rep := &agentv1.SecurityReport{
		TakenUnixMs: time.Now().UnixMilli(),
		Misconfigs:  ms,
	}
	if r.Conntrack != nil {
		if st := r.Conntrack.Status(); st != nil {
			rep.Conntrack = st
		}
	}
	if rep.Conntrack == nil {
		rep.Conntrack = &agentv1.ConntrackStatus{State: "unknown"}
	}
	r.Emit.PutSecurityReport(rep)
}
