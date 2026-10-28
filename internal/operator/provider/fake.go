package provider

import (
	"context"
	"fmt"
	"sync"
)

// Fake is a deterministic provider for tests: it answers from a script and
// records every request, so tests can assert exactly what was sent.
type Fake struct {
	// Script returns the response for the n-th call (0-based). When nil,
	// Responses is used in order.
	Script    func(n int, req Request) (Response, error)
	Responses []Response
	IsLocal   bool
	// Block makes Complete wait for the context to end (an unreachable AI).
	Block bool
	// ModelID is reported by Model (default "fake-model").
	ModelID string

	mu       sync.Mutex
	requests []Request
}

func (f *Fake) Name() string { return "fake" }
func (f *Fake) Local() bool  { return f.IsLocal }

// Model returns ModelID or "fake-model".
func (f *Fake) Model() string {
	if f.ModelID != "" {
		return f.ModelID
	}
	return "fake-model"
}

// Requests returns a copy of the requests seen so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

func (f *Fake) Complete(ctx context.Context, req Request) (Response, error) {
	f.mu.Lock()
	n := len(f.requests)
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.Block {
		<-ctx.Done()
		return Response{}, fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
	}
	if f.Script != nil {
		return f.Script(n, req)
	}
	if n >= len(f.Responses) {
		return Response{}, fmt.Errorf("%w: fake has no response for call %d", ErrBadResponse, n)
	}
	return f.Responses[n], nil
}
