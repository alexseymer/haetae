//go:build integration

package alerts_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/alerts"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

func TestNewCVEAdvisoryToAlert(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "cve-org")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, "h", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.EnrollHost(ctx, tok, store.HostInfo{Hostname: "h1"}, func(uuid.UUID) (string, error) {
		return "serial-cve", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Hour)
	if err := s.ReplaceContainers(ctx, org.ID, h.ID, []store.Container{{
		ContainerID: "web1", Name: "web", State: "running", Image: "app:1", StartedAt: &started,
	}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSbom(ctx, org.ID, store.SaveSbomInput{
		HostID: h.ID, ContainerID: "web1", Image: "app:1", ImageID: "sha256:app",
		Trigger: "start", Packages: []store.SbomPackage{{Name: "libfoo", Version: "1.0.0", Type: "apk"}},
	}); err != nil {
		t.Fatal(err)
	}

	eval := alerts.NewEvaluator(s, alerts.NopNotifier{}, alerts.Config{Interval: 50 * time.Millisecond}, nil)
	eval.EvaluateOnce(ctx)
	if n, _ := s.ListFiringAlerts(ctx, org.ID); len(n) != 0 {
		t.Fatalf("unexpected alerts before advisory: %d", len(n))
	}

	start := time.Now()
	if err := s.ReplaceFindings(ctx, org.ID, "sha256:app", []store.Finding{{
		VulnID: "CVE-2024-NEW", Package: "libfoo", Version: "1.0.0",
		FixedVersion: "1.0.1", Severity: store.FindingCritical,
	}}); err != nil {
		t.Fatal(err)
	}
	eval.Nudge()
	eval.EvaluateOnce(ctx)
	dur := time.Since(start)
	fmt.Printf("MEASURE new-advisory-to-alert: %s\n", dur)

	alertsList, err := s.ListFiringAlerts(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(alertsList) != 1 || alertsList[0].Rule != store.RuleNewCVE {
		t.Fatalf("alerts = %+v", alertsList)
	}
	if alertsList[0].DedupeKey != "new_cve|web1" {
		t.Fatalf("dedupe = %s", alertsList[0].DedupeKey)
	}
	if dur > 15*time.Minute {
		t.Fatalf("too slow: %s", dur)
	}
}
