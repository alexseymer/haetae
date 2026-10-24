//go:build integration

package store_test

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
	"github.com/google/uuid"
)

func TestMigrationsIdempotent(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	url := storetest.URL(t)

	s2, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(s2.Close)

	if s.Pool() == nil || s2.Pool() == nil {
		t.Fatal("expected non-nil pools")
	}
}

func TestDefaultOrgStableAndConcurrent(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	o1, err := s.DefaultOrg(ctx)
	if err != nil {
		t.Fatalf("DefaultOrg: %v", err)
	}
	o2, err := s.DefaultOrg(ctx)
	if err != nil {
		t.Fatalf("DefaultOrg second: %v", err)
	}
	if o1.ID != o2.ID || o1.Name != "default" {
		t.Fatalf("DefaultOrg not stable: %+v vs %+v", o1, o2)
	}

	s2 := storetest.New(t)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ids  = map[uuid.UUID]struct{}{}
		errs []error
	)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := s2.DefaultOrg(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			ids[o.ID] = struct{}{}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("concurrent DefaultOrg errors: %v", errs)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 org from 10 concurrent DefaultOrg calls, got %d", len(ids))
	}
}

func TestEnrollmentTokenFormat(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "tok-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	plaintext, _, err := s.CreateEnrollmentToken(ctx, org.ID, "web", time.Hour)
	if err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}
	if !regexp.MustCompile(`^T-[a-z2-7]{26}$`).MatchString(plaintext) {
		t.Fatalf("token format: %q", plaintext)
	}
}

func TestEnrollHostSuccessAndReuse(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "enroll-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	token, _, err := s.CreateEnrollmentToken(ctx, org.ID, "api", time.Hour)
	if err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}

	info := store.HostInfo{Hostname: "h1", OS: "linux", Kernel: "6.1", Arch: "amd64", DockerVersion: "27", AgentVersion: "0.1"}
	h, err := s.EnrollHost(ctx, token, info, func(hostID uuid.UUID) (string, error) {
		return "serial-" + hostID.String(), nil
	})
	if err != nil {
		t.Fatalf("EnrollHost: %v", err)
	}
	if h.Name != "api" || h.OrgID != org.ID || h.CertSerial == "" {
		t.Fatalf("unexpected host: %+v", h)
	}

	_, err = s.EnrollHost(ctx, token, info, func(uuid.UUID) (string, error) { return "x", nil })
	if !errors.Is(err, store.ErrTokenInvalid) {
		t.Fatalf("second enroll: want ErrTokenInvalid, got %v", err)
	}
}

func TestEnrollHostExpiredToken(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "exp-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	token, _, err := s.CreateEnrollmentToken(ctx, org.ID, "old", -time.Second)
	if err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}
	_, err = s.EnrollHost(ctx, token, store.HostInfo{}, func(uuid.UUID) (string, error) { return "s", nil })
	if !errors.Is(err, store.ErrTokenInvalid) {
		t.Fatalf("expired token: want ErrTokenInvalid, got %v", err)
	}
}

func TestEnrollHostIssueErrorRollsBack(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "issue-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	token, _, err := s.CreateEnrollmentToken(ctx, org.ID, "svc", time.Hour)
	if err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}

	boom := errors.New("issue failed")
	_, err = s.EnrollHost(ctx, token, store.HostInfo{Hostname: "x"}, func(uuid.UUID) (string, error) {
		return "", boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("issue error: want wrapped boom, got %v", err)
	}

	h, err := s.EnrollHost(ctx, token, store.HostInfo{Hostname: "x"}, func(hostID uuid.UUID) (string, error) {
		return "ok-" + hostID.String(), nil
	})
	if err != nil {
		t.Fatalf("retry after issue failure: %v", err)
	}
	if h.Name != "svc" {
		t.Fatalf("unexpected host name %q", h.Name)
	}
}

func TestEnrollHostDuplicateNameLeavesTokenUsable(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "dup-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	tok1, _, err := s.CreateEnrollmentToken(ctx, org.ID, "same", time.Hour)
	if err != nil {
		t.Fatalf("CreateEnrollmentToken 1: %v", err)
	}
	_, err = s.EnrollHost(ctx, tok1, store.HostInfo{}, func(uuid.UUID) (string, error) { return "s1", nil })
	if err != nil {
		t.Fatalf("first enroll: %v", err)
	}

	tok2, _, err := s.CreateEnrollmentToken(ctx, org.ID, "same", time.Hour)
	if err != nil {
		t.Fatalf("CreateEnrollmentToken 2: %v", err)
	}
	_, err = s.EnrollHost(ctx, tok2, store.HostInfo{}, func(uuid.UUID) (string, error) { return "s2", nil })
	if !errors.Is(err, store.ErrHostNameTaken) {
		t.Fatalf("dup name: want ErrHostNameTaken, got %v", err)
	}

	// Retry must still be ErrHostNameTaken (not ErrTokenInvalid), proving the token was left unused.
	_, err = s.EnrollHost(ctx, tok2, store.HostInfo{}, func(uuid.UUID) (string, error) { return "s2b", nil })
	if !errors.Is(err, store.ErrHostNameTaken) {
		t.Fatalf("retry dup: want ErrHostNameTaken (token still usable), got %v", err)
	}
}

func TestListHostsOrgIsolation(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	orgA, err := s.CreateOrg(ctx, "org-a")
	if err != nil {
		t.Fatalf("CreateOrg A: %v", err)
	}
	orgB, err := s.CreateOrg(ctx, "org-b")
	if err != nil {
		t.Fatalf("CreateOrg B: %v", err)
	}

	enroll := func(orgID uuid.UUID, name string) {
		t.Helper()
		tok, _, err := s.CreateEnrollmentToken(ctx, orgID, name, time.Hour)
		if err != nil {
			t.Fatalf("token %s: %v", name, err)
		}
		_, err = s.EnrollHost(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, error) {
			return "serial-" + name, nil
		})
		if err != nil {
			t.Fatalf("enroll %s: %v", name, err)
		}
	}
	enroll(orgA.ID, "a1")
	enroll(orgB.ID, "b1")

	listA, err := s.ListHosts(ctx, orgA.ID)
	if err != nil {
		t.Fatalf("ListHosts A: %v", err)
	}
	listB, err := s.ListHosts(ctx, orgB.ID)
	if err != nil {
		t.Fatalf("ListHosts B: %v", err)
	}
	if len(listA) != 1 || listA[0].Name != "a1" {
		t.Fatalf("org A hosts: %+v", listA)
	}
	if len(listB) != 1 || listB[0].Name != "b1" {
		t.Fatalf("org B hosts: %+v", listB)
	}
	for _, h := range listA {
		if h.OrgID != orgA.ID {
			t.Fatalf("org A list leaked: %+v", h)
		}
	}

	// GetHost is scoped too: org B cannot read org A's host by id.
	if _, err := s.GetHost(ctx, orgB.ID, listA[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetHost across orgs: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetHost(ctx, orgA.ID, listA[0].ID); err != nil {
		t.Fatalf("GetHost in own org: %v", err)
	}
}

func TestConnectionLifecycle(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "conn-org")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, "node", time.Hour)
	if err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}
	h, err := s.EnrollHost(ctx, tok, store.HostInfo{Hostname: "old"}, func(uuid.UUID) (string, error) {
		return "ser", nil
	})
	if err != nil {
		t.Fatalf("EnrollHost: %v", err)
	}

	at := time.Now().UTC().Truncate(time.Millisecond)
	info := store.HostInfo{Hostname: "newhost", OS: "linux", Kernel: "6.8", Arch: "arm64", DockerVersion: "28", AgentVersion: "0.2"}
	if err := s.MarkConnected(ctx, h.ID, info, at); err != nil {
		t.Fatalf("MarkConnected: %v", err)
	}
	got, err := s.GetHost(ctx, h.OrgID, h.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if !got.Connected {
		t.Fatal("expected connected")
	}
	if got.LastSeenAt == nil || got.LastSeenAt.UTC().Sub(at).Abs() > time.Second {
		t.Fatalf("last_seen_at: got %v want ~%v", got.LastSeenAt, at)
	}
	if got.Info != info {
		t.Fatalf("info: got %+v want %+v", got.Info, info)
	}

	seenAt := at.Add(time.Minute)
	if err := s.MarkSeen(ctx, h.ID, seenAt); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	got, err = s.GetHost(ctx, h.OrgID, h.ID)
	if err != nil {
		t.Fatalf("GetHost after seen: %v", err)
	}
	if got.LastSeenAt == nil || got.LastSeenAt.UTC().Sub(seenAt).Abs() > time.Second {
		t.Fatalf("last_seen_at after MarkSeen: %v", got.LastSeenAt)
	}

	if err := s.MarkDisconnected(ctx, h.ID); err != nil {
		t.Fatalf("MarkDisconnected: %v", err)
	}
	got, err = s.GetHost(ctx, h.OrgID, h.ID)
	if err != nil {
		t.Fatalf("GetHost after disconnect: %v", err)
	}
	if got.Connected {
		t.Fatal("expected disconnected")
	}

	if err := s.MarkConnected(ctx, h.ID, info, at); err != nil {
		t.Fatalf("MarkConnected again: %v", err)
	}
	if err := s.ResetConnections(ctx); err != nil {
		t.Fatalf("ResetConnections: %v", err)
	}
	got, err = s.GetHost(ctx, h.OrgID, h.ID)
	if err != nil {
		t.Fatalf("GetHost after reset: %v", err)
	}
	if got.Connected {
		t.Fatal("expected ResetConnections to clear connected")
	}
}
