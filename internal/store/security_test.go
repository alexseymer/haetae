//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

func TestReplaceMisconfigsPreservesFirstSeenAndResolves(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "sec-org")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, "sec-host", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.EnrollHost(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, error) { return "serial", nil })
	if err != nil {
		t.Fatal(err)
	}

	m1 := store.Misconfig{ContainerID: "c1", Kind: "privileged", Severity: "high", Detail: "privileged=true"}
	if err := s.ReplaceMisconfigs(ctx, org.ID, host.ID, []store.Misconfig{m1}); err != nil {
		t.Fatal(err)
	}
	open, err := s.ListMisconfigs(ctx, org.ID, host.ID, true)
	if err != nil || len(open) != 1 {
		t.Fatalf("open=%v err=%v", open, err)
	}
	first := open[0].FirstSeen
	time.Sleep(20 * time.Millisecond)

	m1b := store.Misconfig{ContainerID: "c1", Kind: "privileged", Severity: "high", Detail: "privileged=true"}
	m2 := store.Misconfig{ContainerID: "c1", Kind: "root_user", Severity: "low", Detail: "user=root"}
	if err := s.ReplaceMisconfigs(ctx, org.ID, host.ID, []store.Misconfig{m1b, m2}); err != nil {
		t.Fatal(err)
	}
	open, err = s.ListMisconfigs(ctx, org.ID, host.ID, true)
	if err != nil || len(open) != 2 {
		t.Fatalf("open=%v err=%v", open, err)
	}
	for _, m := range open {
		if m.Kind == "privileged" && !m.FirstSeen.Equal(first) {
			t.Fatalf("first_seen changed: %v → %v", first, m.FirstSeen)
		}
	}

	// Drop privileged → resolved.
	if err := s.ReplaceMisconfigs(ctx, org.ID, host.ID, []store.Misconfig{m2}); err != nil {
		t.Fatal(err)
	}
	open, err = s.ListMisconfigs(ctx, org.ID, host.ID, true)
	if err != nil || len(open) != 1 || open[0].Kind != "root_user" {
		t.Fatalf("open after resolve=%v err=%v", open, err)
	}
	all, err := s.ListMisconfigs(ctx, org.ID, host.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	var resolved bool
	for _, m := range all {
		if m.Kind == "privileged" && m.ResolvedAt != nil {
			resolved = true
		}
	}
	if !resolved {
		t.Fatal("privileged not resolved")
	}
}

func TestSecurityRLSIsolation(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	mk := func(name string) (store.Org, store.Host) {
		org, err := s.CreateOrg(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, name+"-h", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		h, err := s.EnrollHost(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, error) { return "s-" + name, nil })
		if err != nil {
			t.Fatal(err)
		}
		_ = s.ReplaceContainers(ctx, org.ID, h.ID, []store.Container{{ContainerID: name + "-c", Name: "c"}}, true)
		_ = s.ReplaceMisconfigs(ctx, org.ID, h.ID, []store.Misconfig{{
			ContainerID: name + "-c", Kind: "privileged", Severity: "high", Detail: "p",
		}})
		_ = s.UpsertHostSecurity(ctx, store.HostSecurity{
			HostID: h.ID, OrgID: org.ID, ConntrackState: "ok",
		})
		_ = s.UpsertOutbound(ctx, org.ID, h.ID, []store.OutboundConn{{
			ContainerID: name + "-c", DstIP: "1.2.3.4", DstPort: 443, Proto: "tcp", Count: 1,
		}})
		_ = s.UpsertImageUpdate(ctx, store.ImageUpdate{
			OrgID: org.ID, HostID: h.ID, ContainerID: name + "-c",
			Image: "busybox", Decision: "current", Reason: "same",
		})
		return org, h
	}
	orgA, hostA := mk("seca")
	orgB, hostB := mk("secb")

	a := s.ForOrg(orgA.ID)
	if ms, _ := a.ListMisconfigs(ctx, orgB.ID, hostB.ID, true); len(ms) != 0 {
		t.Fatalf("A sees B misconfigs: %v", ms)
	}
	if ms, _ := a.ListMisconfigs(ctx, orgA.ID, hostA.ID, true); len(ms) != 1 {
		t.Fatalf("A own misconfigs: %v", ms)
	}
	if _, err := a.GetHostSecurity(ctx, orgB.ID, hostB.ID); err == nil {
		t.Fatal("A got B host security")
	}
	if out, _ := a.ListOutbound(ctx, orgB.ID, hostB.ID, 10); len(out) != 0 {
		t.Fatalf("A sees B outbound: %v", out)
	}
	if ups, _ := a.ListImageUpdates(ctx, orgB.ID, hostB.ID); len(ups) != 0 {
		t.Fatalf("A sees B image updates: %v", ups)
	}
}

func TestUpsertOutboundBounded(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, _ := s.CreateOrg(ctx, "out-org")
	tok, _, _ := s.CreateEnrollmentToken(ctx, org.ID, "out-h", time.Hour)
	host, _ := s.EnrollHost(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, error) { return "s", nil })

	var batch []store.OutboundConn
	for i := 0; i < 10; i++ {
		batch = append(batch, store.OutboundConn{
			ContainerID: "c", DstIP: "1.2.3.4", DstPort: 1000 + i, Proto: "tcp", Count: 1,
		})
	}
	if err := s.UpsertOutbound(ctx, org.ID, host.ID, batch); err != nil {
		t.Fatal(err)
	}
	out, err := s.ListOutbound(ctx, org.ID, host.ID, 100)
	if err != nil || len(out) != 10 {
		t.Fatalf("got %d err=%v", len(out), err)
	}
}
