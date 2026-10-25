package gateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// maxPending bounds in-flight commands per host so a stuck agent cannot make
// the server accumulate waiters without limit.
const maxPending = 64

var errSessionClosed = errors.New("gateway: session closed")

type session struct {
	hostID   uuid.UUID
	orgID    uuid.UUID
	cancel   context.CancelCauseFunc
	outbox   chan *agentv1.Command
	green    chan []string          // latest green set; a newer one replaces an unsent one
	watch    chan *agentv1.WatchSet // latest watch set; replace-latest
	lastSeen atomic.Int64           // unix nanoseconds

	eventLim *tokenBucket
	logLim   *tokenBucket

	droppedEvents atomic.Int64
	droppedLogs   atomic.Int64

	warnMu          sync.Mutex
	lastUnknownWarn time.Time

	mu      sync.Mutex
	pending map[string]chan *agentv1.CommandResult
	closed  bool
}

func newSession(hostID uuid.UUID, cancel context.CancelCauseFunc) *session {
	s := &session{
		hostID:   hostID,
		cancel:   cancel,
		outbox:   make(chan *agentv1.Command, 16),
		green:    make(chan []string, 1),
		watch:    make(chan *agentv1.WatchSet, 1),
		eventLim: newTokenBucket(deployEventRate, deployEventRate),
		logLim:   newTokenBucket(deployLogRate, deployLogRate),
		pending:  make(map[string]chan *agentv1.CommandResult),
	}
	s.touch()
	return s
}

// setGreen queues a green set, replacing one that has not been sent yet.
func (s *session) setGreen(ids []string) {
	for {
		select {
		case s.green <- ids:
			return
		default:
		}
		select {
		case <-s.green:
		default:
		}
	}
}

// setWatch queues a watch set, replacing one that has not been sent yet.
func (s *session) setWatch(ws *agentv1.WatchSet) {
	if ws == nil {
		ws = &agentv1.WatchSet{}
	}
	for {
		select {
		case s.watch <- ws:
			return
		default:
		}
		select {
		case <-s.watch:
		default:
		}
	}
}

func (s *session) touch() { s.lastSeen.Store(time.Now().UnixNano()) }

func (s *session) idle() time.Duration {
	return time.Since(time.Unix(0, s.lastSeen.Load()))
}

func (s *session) call(ctx context.Context, cmd *agentv1.Command) (*agentv1.CommandResult, error) {
	id := uuid.NewString()
	cmd.Id = id
	ch := make(chan *agentv1.CommandResult, 1)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errSessionClosed
	}
	if len(s.pending) >= maxPending {
		s.mu.Unlock()
		return nil, errors.New("gateway: too many commands in flight for host")
	}
	s.pending[id] = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	select {
	case s.outbox <- cmd:
	case <-ch: // closed by close(): the session ended while the outbox was full
		return nil, errSessionClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case res, ok := <-ch:
		if !ok {
			return nil, errSessionClosed
		}
		return res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *session) deliver(res *agentv1.CommandResult) {
	// Hold the lock while sending so close cannot close ch underneath us.
	// The channel is buffered and written at most once, so this never blocks.
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.pending[res.GetCommandId()]
	if ch == nil {
		return // late or unknown result
	}
	select {
	case ch <- res:
	default:
	}
}

func (s *session) close() {
	s.cancel(errSessionClosed)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
}
