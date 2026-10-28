package operator

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/operator/provider"
	"github.com/alexseymer/haetae/internal/store"
)

// HostCommander delivers a command to a connected agent and returns its
// result. It has the same shape as updates.Sender and deploys.Sender, so the
// gateway satisfies all of them.
type HostCommander interface {
	Send(ctx context.Context, hostID uuid.UUID, cmd *agentv1.Command) (*agentv1.CommandResult, error)
}

// Config configures a Service.
type Config struct {
	Store *store.Store
	// Provider is the AI backend; nil means the AI is off.
	Provider  provider.Provider
	Commander HostCommander
	Log       *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
	// CallTimeout bounds each provider call and each host command (default 60 s).
	CallTimeout time.Duration
	// Redact removes secrets and personal data from one string before it is
	// handed to a non-local provider. It defaults to the internal/redact
	// pipeline; an error or a timeout makes the operation fail closed.
	Redact func(string) (string, error)
}

// Service implements Operator.
type Service struct {
	cfg Config
	// actMu serialises the rate-limit check and the audit insert of restarts,
	// so concurrent proposals cannot exceed the per-container limit.
	actMu sync.Mutex

	failMu   sync.Mutex
	failLast map[string]time.Time // class -> last logged failure
}

var _ Operator = (*Service)(nil)

// New returns a Service. A nil Provider turns the AI off.
func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = 60 * time.Second
	}
	if cfg.Redact == nil {
		cfg.Redact = defaultRedact()
	}
	return &Service{cfg: cfg}
}

// Available implements Operator.
func (s *Service) Available(ctx context.Context, orgID uuid.UUID) (bool, string) {
	err := s.gate(ctx, orgID)
	switch {
	case err == nil:
		return true, ""
	case errors.Is(err, ErrCapped):
		return false, "daily usage cap reached"
	case errors.Is(err, errNotConfigured):
		return false, "not configured"
	case errors.Is(err, errTurnedOff):
		return false, "turned off"
	}
	return false, "temporarily unavailable"
}

var (
	errNotConfigured = errors.New("not configured")
	errTurnedOff     = errors.New("turned off")
)

// gate returns nil when AI work may run for the org now. The returned errors
// wrap ErrDisabled or ErrCapped.
func (s *Service) gate(ctx context.Context, orgID uuid.UUID) error {
	if s.cfg.Provider == nil {
		return joinErr(ErrDisabled, errNotConfigured)
	}
	if s.cfg.Store == nil {
		return joinErr(ErrDisabled, errNotConfigured)
	}
	set, err := s.cfg.Store.GetOperatorSettings(ctx, orgID)
	if err != nil {
		return err
	}
	if !set.AIEnabled {
		return joinErr(ErrDisabled, errTurnedOff)
	}
	u, err := s.cfg.Store.UsageToday(ctx, orgID, s.cfg.Now())
	if err != nil {
		return err
	}
	if u.Total() >= set.DailyTokenCap {
		return ErrCapped
	}
	return nil
}

func joinErr(a, b error) error { return errors.Join(a, b) }

func (s *Service) recordUsage(ctx context.Context, orgID uuid.UUID, in, out int64) {
	if err := s.cfg.Store.AddUsage(ctx, orgID, s.cfg.Now(), in, out); err != nil {
		s.cfg.Log.Warn("operator: record usage failed", "err", err)
	}
}
