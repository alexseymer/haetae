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

func TestRenewHostCert(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.DefaultOrg(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, "renew", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	exp1 := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	h, err := s.EnrollHostWithExpiry(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, time.Time, error) { return "aa", exp1, nil })
	if err != nil {
		t.Fatal(err)
	}
	if h.CertExpiresAt == nil || !h.CertExpiresAt.Equal(exp1) {
		t.Fatalf("CertExpiresAt = %v, want %v", h.CertExpiresAt, exp1)
	}

	exp2 := exp1.Add(48 * time.Hour)
	// A serial that is not the host's is refused and changes nothing.
	if err := s.RenewHostCert(ctx, h.ID, "zz", "bb", exp2, time.Hour); !errors.Is(err, store.ErrCertNotCurrent) {
		t.Fatalf("foreign serial: %v", err)
	}
	if err := s.RenewHostCert(ctx, h.ID, "aa", "bb", exp2, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, _ := s.HostForAgent(ctx, h.ID)
	if got.CertSerial != "bb" || got.PrevCertSerial != "aa" || got.CertExpiresAt == nil || !got.CertExpiresAt.Equal(exp2) {
		t.Fatalf("after renew: %+v", got)
	}
	// The old certificate may renew again within its grace (lost reply).
	if err := s.RenewHostCert(ctx, h.ID, "aa", "cc", exp2, time.Hour); err != nil {
		t.Fatalf("renew from previous serial: %v", err)
	}
	// ...but a serial that is neither current nor previous may not.
	if err := s.RenewHostCert(ctx, h.ID, "bb", "dd", exp2, time.Hour); !errors.Is(err, store.ErrCertNotCurrent) {
		t.Fatalf("dropped serial: %v", err)
	}
	if err := s.ForgetPrevCert(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RenewHostCert(ctx, h.ID, "aa", "ee", exp2, time.Hour); !errors.Is(err, store.ErrCertNotCurrent) {
		t.Fatalf("forgotten previous serial: %v", err)
	}
	// A revoked host cannot renew.
	if _, err := s.RevokeHost(ctx, org.ID, h.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RenewHostCert(ctx, h.ID, "cc", "ff", exp2, time.Hour); !errors.Is(err, store.ErrCertNotCurrent) {
		t.Fatalf("revoked host: %v", err)
	}
}

func TestRenewHostCertGraceExpires(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, _ := s.DefaultOrg(ctx)
	tok, _, _ := s.CreateEnrollmentToken(ctx, org.ID, "grace", time.Hour)
	h, err := s.EnrollHostWithExpiry(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, time.Time, error) { return "aa", time.Now().Add(time.Hour), nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RenewHostCert(ctx, h.ID, "aa", "bb", time.Now().Add(time.Hour), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := s.RenewHostCert(ctx, h.ID, "aa", "cc", time.Now().Add(time.Hour), time.Hour); !errors.Is(err, store.ErrCertNotCurrent) {
		t.Fatalf("expired grace: %v", err)
	}
}
