//go:build integration

package operator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/operator/provider"
	"github.com/alexseymer/haetae/internal/store"
)

// A model statement that cites the failed-scan evidence and calls the
// container clean must not hide the scan-failure line.
func TestSecurityDigestFailedScanSurvivesFalseCleanStatement(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.s.RecordFailedScan(ctx, e.org.ID, store.FailedScanInput{
		HostID: e.host.ID, ContainerID: e.okC, Image: "nginx", ImageID: "sha256:web",
		Trigger: "daily", Status: store.ScanFailed, Error: "helper crashed",
	}); err != nil {
		t.Fatal(err)
	}
	// Evidence: ev_1 is the overview, ev_2 the scan problems of the host.
	f := fakeOf(final(`{"statements":[{"text":"Container web has 0 critical findings.","evidence":["ev_2"]}]}`))
	d, err := e.svc(f).SecurityDigest(ctx, e.org.ID, operator.SecurityScope{})
	if err != nil {
		t.Fatal(err)
	}
	if !d.UsedAI {
		t.Fatalf("digest not from the AI: %q", d.Reason)
	}
	found := false
	for _, s := range d.Statements {
		if strings.Contains(s.Text, "Scan failed on 1 container") {
			found = true
		}
	}
	if !found {
		t.Fatalf("scan-failure line missing from %+v", d.Statements)
	}
}

// A container that was never scanned makes the digest need attention, so the
// scheduled digest posts on that day.
func TestSecurityDigestNeedsAttentionWhenNeverScanned(t *testing.T) {
	e := newEnv(t)
	d, err := e.svc(&provider.Fake{IsLocal: true}).RuleSecurityDigest(context.Background(), e.org.ID, operator.SecurityScope{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Quiet || !d.NeedsAttention {
		t.Fatalf("never-scanned digest: quiet=%v needsAttention=%v", d.Quiet, d.NeedsAttention)
	}
}
