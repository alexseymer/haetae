package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The server waits for a database that is not up yet instead of exiting, and
// stops waiting when asked to shut down.
func TestOpenStoreRetriesUntilCancelled(t *testing.T) {
	var out strings.Builder
	log := slog.New(slog.NewTextHandler(&out, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	st, err := openStore(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1", log)
	if err == nil || st != nil {
		t.Fatalf("openStore against nothing = %v, %v", st, err)
	}
	if took := time.Since(start); took < 2*time.Second || took > 8*time.Second {
		t.Fatalf("gave up after %v; it should keep trying until the context ends (about 3 s)", took)
	}
	if !strings.Contains(out.String(), "database not ready, retrying") {
		t.Fatalf("no retry message logged: %q", out.String())
	}
}
