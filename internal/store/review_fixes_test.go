//go:build integration

package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

func seedScannedContainer(t *testing.T, s *store.Store, imageID string) (store.Org, store.Host) {
	t.Helper()
	ctx := context.Background()
	org, h := seedOrgHost(t, s)
	if err := s.ReplaceContainers(ctx, org.ID, h.ID, []store.Container{{
		ContainerID: "c1", Name: "web", State: "running", Image: "img:1",
	}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSbom(ctx, org.ID, store.SaveSbomInput{
		HostID: h.ID, ContainerID: "c1", Image: "img:1", ImageID: imageID, Trigger: "start",
		Packages: []store.SbomPackage{{Name: "p", Version: "1", Type: "deb"}},
	}); err != nil {
		t.Fatal(err)
	}
	return org, h
}

// Scan ok but not matched must never read as "no vulnerabilities".
func TestScanOKButNotMatchedIsPendingNotClean(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, h := seedScannedContainer(t, s, "img1")

	check := func(wantMatch string, wantPending int, wantFailed bool) {
		t.Helper()
		views, err := s.ContainerVulnViews(ctx, org.ID, h.ID)
		if err != nil || len(views) != 1 {
			t.Fatalf("views=%v err=%v", views, err)
		}
		if views[0].MatchStatus != wantMatch {
			t.Fatalf("match status = %q, want %q", views[0].MatchStatus, wantMatch)
		}
		sum, err := s.HostVulnBadgeSummary(ctx, org.ID, h.ID)
		if err != nil {
			t.Fatal(err)
		}
		if sum.Pending != wantPending || sum.ScanFailed != wantFailed {
			t.Fatalf("summary = %+v, want pending=%d failed=%v", sum, wantPending, wantFailed)
		}
	}
	check("none", 1, false) // no match row at all
	if err := s.SetMatchState(ctx, org.ID, "img1", store.MatchPending, "", nil); err != nil {
		t.Fatal(err)
	}
	check("pending", 1, false)
	if err := s.SetMatchState(ctx, org.ID, "img1", store.MatchFailed, "boom", nil); err != nil {
		t.Fatal(err)
	}
	check("failed", 0, true)
	if err := s.SetMatchState(ctx, org.ID, "img1", store.MatchOK, "", nil); err != nil {
		t.Fatal(err)
	}
	check("ok", 0, false)
}

func TestListUnmatchedSboms(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, _ := seedScannedContainer(t, s, "img-un")
	list := func() int {
		rows, err := s.ListUnmatchedSboms(ctx, uuid.Nil, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range rows {
			if r.OrgID == org.ID && r.ImageID == "img-un" {
				n++
			}
		}
		return n
	}
	if list() != 1 {
		t.Fatal("sbom without match state must be listed")
	}
	_ = s.SetMatchState(ctx, org.ID, "img-un", store.MatchPending, "", nil)
	if list() != 1 {
		t.Fatal("pending sbom must be listed")
	}
	_ = s.SetMatchState(ctx, org.ID, "img-un", store.MatchOK, "", nil)
	if list() != 0 {
		t.Fatal("matched sbom must not be listed")
	}
}

func TestFindingsSeverityOrderAndPkgTypeKey(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, h := seedScannedContainer(t, s, "img2")
	err := s.ReplaceFindings(ctx, org.ID, "img2", []store.Finding{
		{VulnID: "CVE-L", Package: "p", Version: "1", PkgType: "deb", Severity: store.FindingLow},
		{VulnID: "CVE-M", Package: "p", Version: "1", PkgType: "deb", Severity: store.FindingMedium},
		{VulnID: "CVE-C", Package: "p", Version: "1", PkgType: "deb", Severity: store.FindingCritical},
		{VulnID: "CVE-H", Package: "p", Version: "1", PkgType: "deb", Severity: store.FindingHigh},
		// Same vuln/package/version in two package types: both kept.
		{VulnID: "CVE-X", Package: "q", Version: "2", PkgType: "npm", Severity: store.FindingHigh},
		{VulnID: "CVE-X", Package: "q", Version: "2", PkgType: "python", Severity: store.FindingHigh},
		// Exact duplicate in one batch must not break the transaction.
		{VulnID: "CVE-X", Package: "q", Version: "2", PkgType: "python", Severity: store.FindingHigh},
	})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := s.FindingsForContainer(ctx, org.ID, h.ID, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 6 {
		t.Fatalf("want 6 findings (pkg_type in key, duplicate collapsed), got %d: %+v", len(fs), fs)
	}
	rank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	for i := 1; i < len(fs); i++ {
		if rank[fs[i-1].Severity] > rank[fs[i].Severity] {
			t.Fatalf("not ordered by severity: %+v", fs)
		}
	}
	hf, err := s.ListFindingsForHost(ctx, org.ID, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hf[0].Severity != store.FindingCritical || hf[len(hf)-1].Severity != store.FindingLow {
		t.Fatalf("host findings not severity ordered: %+v", hf)
	}
}

func TestImageVulnIDsNotTruncated(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, _ := seedScannedContainer(t, s, "img3")
	var fs []store.Finding
	for i := 0; i < 250; i++ {
		fs = append(fs, store.Finding{VulnID: fmt.Sprintf("CVE-%04d", i), Package: "p", Version: "1", PkgType: "deb", Severity: store.FindingCritical})
	}
	if err := s.ReplaceFindings(ctx, org.ID, "img3", fs); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ImageVulnIDs(ctx, org.ID, "img3", true)
	if err != nil || len(ids) != 250 {
		t.Fatalf("ids=%d err=%v, want 250", len(ids), err)
	}
}

func TestImageUpdateFirstSeenAndPullBookkeeping(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, h := seedScannedContainer(t, s, "img4")
	u := store.ImageUpdate{
		OrgID: org.ID, HostID: h.ID, ContainerID: "c1", Image: "img:1",
		RunningDigest: "sha256:run", CandidateDigest: "sha256:c1", Decision: "wait", Reason: "young",
	}
	if err := s.UpsertImageUpdate(ctx, u); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetImageUpdate(ctx, org.ID, h.ID, "c1")
	if err != nil || a.CandidateFirstSeen == nil {
		t.Fatalf("first seen not set: %+v err=%v", a, err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := s.UpsertImageUpdate(ctx, u); err != nil {
		t.Fatal(err)
	}
	b, _ := s.GetImageUpdate(ctx, org.ID, h.ID, "c1")
	if !b.CandidateFirstSeen.Equal(*a.CandidateFirstSeen) {
		t.Fatalf("first seen changed for same digest: %v -> %v", a.CandidateFirstSeen, b.CandidateFirstSeen)
	}

	if err := s.MarkImagePull(ctx, org.ID, h.ID, "c1", "sha256:c1", true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	u.CandidateDigest = "sha256:c2"
	if err := s.UpsertImageUpdate(ctx, u); err != nil {
		t.Fatal(err)
	}
	c, _ := s.GetImageUpdate(ctx, org.ID, h.ID, "c1")
	if !c.CandidateFirstSeen.After(*a.CandidateFirstSeen) {
		t.Fatalf("first seen not reset for new digest: %v -> %v", a.CandidateFirstSeen, c.CandidateFirstSeen)
	}
	if c.PulledDigest != "sha256:c1" || c.PulledAt == nil {
		t.Fatalf("pull bookkeeping lost on upsert: %+v", c)
	}
}

func TestCVEWatermarkPersistsAndNeverMovesBack(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, _ := seedOrgHost(t, s)
	if _, ok, err := s.GetCVEWatermark(ctx, org.ID); err != nil || ok {
		t.Fatalf("fresh org: ok=%v err=%v", ok, err)
	}
	t1 := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.SetCVEWatermark(ctx, org.ID, t1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCVEWatermark(ctx, org.ID, t1.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetCVEWatermark(ctx, org.ID)
	if err != nil || !ok || !got.Equal(t1) {
		t.Fatalf("got=%v ok=%v err=%v want %v", got, ok, err, t1)
	}
}
