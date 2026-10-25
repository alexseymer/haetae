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

func seedOrgHost(t *testing.T, s *store.Store) (store.Org, store.Host) {
	t.Helper()
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, "vuln-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, "h", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.EnrollHost(ctx, tok, store.HostInfo{Hostname: "h"}, func(uuid.UUID) (string, error) {
		return "serial-" + uuid.NewString(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return org, h
}

func TestSaveSbomAndFailedScan(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, h := seedOrgHost(t, s)
	if err := s.ReplaceContainers(ctx, org.ID, h.ID, []store.Container{{
		ContainerID: "c1", Name: "web", State: "running", Image: "img:1",
	}}, true); err != nil {
		t.Fatal(err)
	}

	sc, err := s.SaveSbom(ctx, org.ID, store.SaveSbomInput{
		HostID: h.ID, ContainerID: "c1", Image: "img:1", ImageID: "sha256:abc",
		Trigger: "start", ToolVersion: "syft-1",
		DistroName: "alpine", DistroVersion: "3.20",
		Packages: []store.SbomPackage{{Name: "libfoo", Version: "1.0.0", Type: "apk"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sc.Status != store.ScanOK || sc.PackageCount != 1 {
		t.Fatalf("scan = %+v", sc)
	}
	sb, err := s.LoadSbom(ctx, org.ID, "sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(sb.Packages) != 1 || sb.Packages[0].Name != "libfoo" {
		t.Fatalf("packages = %+v", sb.Packages)
	}

	// Failed scan must not wipe SBOM / findings.
	findings := []store.Finding{{
		VulnID: "CVE-1", Package: "libfoo", Version: "1.0.0", Severity: store.FindingHigh,
	}}
	if err := s.ReplaceFindings(ctx, org.ID, "sha256:abc", findings); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordFailedScan(ctx, org.ID, store.FailedScanInput{
		HostID: h.ID, ContainerID: "c1", Image: "img:1", ImageID: "sha256:abc",
		Trigger: "daily", Status: store.ScanFailed, Error: "oom helper",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.FindingsForContainer(ctx, org.ID, h.ID, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("findings after failed scan = %d, want 1 (must not report zero vulns)", len(got))
	}
	st, err := s.ScanStatusForContainer(ctx, org.ID, h.ID, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != store.ScanFailed {
		t.Fatalf("status = %s, want failed", st.Status)
	}
}

func TestReplaceFindingsKeepsFirstSeen(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, h := seedOrgHost(t, s)
	if err := s.ReplaceContainers(ctx, org.ID, h.ID, []store.Container{{
		ContainerID: "c1", Name: "web", State: "running",
	}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSbom(ctx, org.ID, store.SaveSbomInput{
		HostID: h.ID, ContainerID: "c1", ImageID: "img1", Trigger: "start",
		Packages: []store.SbomPackage{{Name: "p", Version: "1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceFindings(ctx, org.ID, "img1", []store.Finding{
		{VulnID: "CVE-1", Package: "p", Version: "1", Severity: store.FindingHigh},
		{VulnID: "CVE-2", Package: "p", Version: "1", Severity: store.FindingMedium},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := s.FindingsForContainer(ctx, org.ID, h.ID, "c1")
	if err != nil {
		t.Fatal(err)
	}
	var firstSeen time.Time
	for _, f := range first {
		if f.VulnID == "CVE-1" {
			firstSeen = f.FirstSeen
		}
	}
	time.Sleep(20 * time.Millisecond)
	if err := s.ReplaceFindings(ctx, org.ID, "img1", []store.Finding{
		{VulnID: "CVE-1", Package: "p", Version: "1", Severity: store.FindingCritical, FixedVersion: "2"},
	}); err != nil {
		t.Fatal(err)
	}
	second, err := s.FindingsForContainer(ctx, org.ID, h.ID, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].VulnID != "CVE-1" {
		t.Fatalf("after replace: %+v", second)
	}
	if !second[0].FirstSeen.Equal(firstSeen) {
		t.Fatalf("first_seen changed: %v -> %v", firstSeen, second[0].FirstSeen)
	}
	if second[0].Severity != store.FindingCritical || second[0].FixedVersion != "2" {
		t.Fatalf("updated fields: %+v", second[0])
	}
}

func TestSaveSbomRejectsForeignContainer(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, h := seedOrgHost(t, s)
	_, err := s.SaveSbom(ctx, org.ID, store.SaveSbomInput{
		HostID: h.ID, ContainerID: "not-mine", ImageID: "x", Trigger: "start",
	})
	if !errors.Is(err, store.ErrForeignContainer) {
		t.Fatalf("err = %v, want ErrForeignContainer", err)
	}
}

func TestVulnDBUpdate(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	v, err := s.GetVulnDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != store.VulnDBNone {
		t.Fatalf("status = %s", v.Status)
	}
	now := time.Now().UTC()
	if err := s.UpdateVulnDB(ctx, store.VulnDB{
		SourceURL: "http://example/db", BuiltAt: &now, CheckedAt: &now, UpdatedAt: &now,
		Status: store.VulnDBOK,
	}); err != nil {
		t.Fatal(err)
	}
	v2, err := s.GetVulnDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Status != store.VulnDBOK || v2.SourceURL != "http://example/db" {
		t.Fatalf("%+v", v2)
	}
}
