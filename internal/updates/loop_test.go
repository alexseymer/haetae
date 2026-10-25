package updates

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

type memSender struct {
	mu   sync.Mutex
	cmds []*agentv1.Command
}

func (m *memSender) Send(_ context.Context, _ uuid.UUID, cmd *agentv1.Command) (*agentv1.CommandResult, error) {
	m.mu.Lock()
	m.cmds = append(m.cmds, cmd)
	m.mu.Unlock()
	return &agentv1.CommandResult{}, nil
}

type memLoopStore struct {
	auto    bool
	updates map[string][]store.ImageUpdate
}

func (m *memLoopStore) ListOrgIDs(context.Context) ([]uuid.UUID, error) {
	return []uuid.UUID{uuid.MustParse("11111111-1111-1111-1111-111111111111")}, nil
}
func (m *memLoopStore) OrgAutoPull(context.Context, uuid.UUID) (bool, error) {
	return m.auto, nil
}
func (m *memLoopStore) ListImageUpdatesByDecision(_ context.Context, _ uuid.UUID, decisions []string) ([]store.ImageUpdate, error) {
	var out []store.ImageUpdate
	for _, d := range decisions {
		out = append(out, m.updates[d]...)
	}
	return out, nil
}

func TestLoopSendsPullWhenAutoAndEligible(t *testing.T) {
	org := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	host := uuid.New()
	sender := &memSender{}
	st := &memLoopStore{
		auto: true,
		updates: map[string][]store.ImageUpdate{
			DecisionEligible: {{
				OrgID: org, HostID: host, ContainerID: "c1",
				Image: "busybox:latest", CandidateDigest: "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Decision: DecisionEligible,
			}},
		},
	}

	l := NewLoop(st, sender, nil)
	l.tick(context.Background())
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.cmds) != 1 || sender.cmds[0].GetPullImage() == nil {
		t.Fatalf("cmds=%v", sender.cmds)
	}
}

func TestLoopScansYoungCriticalOnce(t *testing.T) {
	org := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	host := uuid.New()
	created := time.Now().Add(-24 * time.Hour)
	sender := &memSender{}
	st := &memLoopStore{
		auto: false,
		updates: map[string][]store.ImageUpdate{
			DecisionWait: {{
				OrgID: org, HostID: host, ContainerID: "c1",
				Image: "busybox:latest", RunningDigest: "sha256:run",
				CandidateDigest:  "sha256:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				CandidateCreated: &created,
				Decision:         DecisionWait,
			}},
		},
	}
	l := NewLoop(st, sender, nil)
	l.Lookup = fakeLookup{
		running: []Finding{{VulnID: "CVE-1", Severity: "critical"}},
		candOK:  false,
	}
	l.tick(context.Background())
	l.tick(context.Background()) // second tick must not re-send within TTL
	sender.mu.Lock()
	defer sender.mu.Unlock()
	n := 0
	for _, c := range sender.cmds {
		if c.GetScanImage() != nil {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("ScanImage count=%d cmds=%v", n, sender.cmds)
	}
}
