//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

func TestRevokeHostHidesItEverywhereAndFreesTheName(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, h := seedBriefingOrg(t, s, "revoke")
	insertIncidentAlert(t, s, org.ID, h.ID, "revoke-title", store.SeverityHigh)

	other, err := s.CreateOrg(ctx, "revoke-other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeHost(ctx, other.ID, h.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org revoke: %v", err)
	}
	if _, err := s.GetHost(ctx, org.ID, h.ID); err != nil {
		t.Fatalf("host vanished after a foreign revoke: %v", err)
	}

	got, err := s.RevokeHost(ctx, org.ID, h.ID)
	if err != nil || got.RevokedAt == nil || got.Connected {
		t.Fatalf("revoke: %+v %v", got, err)
	}
	if _, err := s.RevokeHost(ctx, org.ID, h.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}
	if _, err := s.GetHost(ctx, org.ID, h.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetHost: %v", err)
	}
	if hs, _ := s.ListHosts(ctx, org.ID); len(hs) != 0 {
		t.Fatalf("ListHosts: %d", len(hs))
	}
	// The gateway still sees the row (to refuse it).
	if ag, err := s.HostForAgent(ctx, h.ID); err != nil || ag.RevokedAt == nil {
		t.Fatalf("HostForAgent: %+v %v", ag, err)
	}
	// MarkConnected cannot revive it.
	if err := s.MarkConnected(ctx, h.ID, store.HostInfo{}, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("MarkConnected on revoked: %v", err)
	}

	for _, state := range []string{"", "open", "resolved"} {
		if inc, _ := s.ListIncidents(ctx, org.ID, state, 50); len(inc) != 0 {
			t.Errorf("ListIncidents(%q): %d", state, len(inc))
		}
		if al, _ := s.ListAlerts(ctx, org.ID, state, 50); len(al) != 0 {
			t.Errorf("ListAlerts(%q): %d", state, len(al))
		}
	}
	if al, _ := s.ListFiringAlerts(ctx, org.ID); len(al) != 0 {
		t.Errorf("ListFiringAlerts: %d", len(al))
	}
	f, err := s.BriefingFacts(ctx, org.ID, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if f.Hosts != 0 || len(f.Items) != 0 {
		t.Fatalf("briefing still shows the host: %+v", f)
	}

	// The name can be enrolled again with a fresh token.
	tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, h.Name, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.EnrollHost(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, error) { return "serial-new", nil })
	if err != nil || h2.ID == h.ID {
		t.Fatalf("re-enroll: %+v %v", h2, err)
	}
	// ...but not twice among active hosts.
	tok2, _, _ := s.CreateEnrollmentToken(ctx, org.ID, h.Name, time.Hour)
	if _, err := s.EnrollHost(ctx, tok2, store.HostInfo{}, func(uuid.UUID) (string, error) { return "s3", nil }); !errors.Is(err, store.ErrHostNameTaken) {
		t.Fatalf("duplicate active name: %v", err)
	}
}
