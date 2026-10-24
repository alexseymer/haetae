package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// A caller blocked on a full outbox must return when the session closes,
// even without a deadline on its context.
func TestCallReturnsWhenSessionClosesWithFullOutbox(t *testing.T) {
	s := newSession(uuid.New(), func(error) {})
	for range cap(s.outbox) {
		s.outbox <- &agentv1.Command{}
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.call(context.Background(), &agentv1.Command{})
		done <- err
	}()
	// Wait until call has registered and is blocked on the outbox.
	for deadline := time.Now().Add(2 * time.Second); ; {
		s.mu.Lock()
		n := len(s.pending)
		s.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("call never registered")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond) // from registration to the outbox select
	s.close()

	select {
	case err := <-done:
		if !errors.Is(err, errSessionClosed) {
			t.Fatalf("err = %v, want errSessionClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call still blocked after session close")
	}
}

func TestDeliverAfterCloseDoesNotPanic(t *testing.T) {
	s := newSession(uuid.New(), func(error) {})
	go func() { _, _ = s.call(context.Background(), &agentv1.Command{}) }()
	cmd := <-s.outbox
	s.close()
	s.deliver(&agentv1.CommandResult{CommandId: cmd.GetId()})
}
