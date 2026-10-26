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

func (m *memLoopStore) UpsertImageUpdate(_ context.Context, u store.ImageUpdate) error {
	for d, rows := range m.updates {
		for i, r := range rows {
			if r.HostID == u.HostID && r.ContainerID == u.ContainerID {
				m.updates[d] = append(rows[:i:i], rows[i+1:]...)
				break
			}
		}
	}
	if m.updates == nil {
		m.updates = map[string][]store.ImageUpdate{}
	}
	m.updates[u.Decision] = append(m.updates[u.Decision], u)
	return nil
}

func (m *memLoopStore) MarkImagePull(_ context.Context, _, host uuid.UUID, cid, digest string, ok bool) error {
	now := time.Now()
	for d, rows := range m.updates {
		for i := range rows {
			if rows[i].HostID == host && rows[i].ContainerID == cid {
				rows[i].PullAttemptDigest = digest
				rows[i].PullAttemptedAt = &now
				if ok {
					rows[i].PulledDigest = digest
					rows[i].PulledAt = &now
				}
				m.updates[d] = rows
			}
		}
	}
	return nil
}

func (m *memSender) count(f func(*agentv1.Command) bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.cmds {
		if f(c) {
			n++
		}
	}
	return n
}

func isPull(c *agentv1.Command) bool { return c.GetPullImage() != nil }

func eligibleRow(org, host uuid.UUID) store.ImageUpdate {
	return store.ImageUpdate{
		OrgID: org, HostID: host, ContainerID: "c1", Image: "busybox:latest",
		CandidateDigest: "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Decision:        DecisionEligible,
	}
}

func TestLoopPullsOncePerCandidateDigest(t *testing.T) {
	org := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	host := uuid.New()
	sender := &memSender{}
	st := &memLoopStore{auto: true, updates: map[string][]store.ImageUpdate{
		DecisionEligible: {eligibleRow(org, host)},
	}}
	l := NewLoop(st, sender, nil)
	for i := 0; i < 5; i++ {
		l.tick(context.Background())
	}
	if n := sender.count(isPull); n != 1 {
		t.Fatalf("PullImage sent %d times, want 1", n)
	}
}

type failSender struct{ memSender }

func (f *failSender) Send(ctx context.Context, h uuid.UUID, cmd *agentv1.Command) (*agentv1.CommandResult, error) {
	_, _ = f.memSender.Send(ctx, h, cmd)
	return &agentv1.CommandResult{Error: "pull refused"}, nil
}

func TestLoopRetriesFailedPullNoSoonerThanSixHours(t *testing.T) {
	org := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	host := uuid.New()
	sender := &failSender{}
	st := &memLoopStore{auto: true, updates: map[string][]store.ImageUpdate{
		DecisionEligible: {eligibleRow(org, host)},
	}}
	l := NewLoop(st, sender, nil)
	l.tick(context.Background())
	l.tick(context.Background())
	if n := sender.count(isPull); n != 1 {
		t.Fatalf("failed pull re-sent within 6h: %d", n)
	}
	old := time.Now().Add(-7 * time.Hour)
	st.updates[DecisionEligible][0].PullAttemptedAt = &old
	l.tick(context.Background())
	if n := sender.count(isPull); n != 2 {
		t.Fatalf("expected retry after 6h, sends=%d", n)
	}
}

func TestLoopRedecidesWaitRowsWithoutNewReport(t *testing.T) {
	org := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	host := uuid.New()
	old := time.Now().Add(-72 * time.Hour)
	row := eligibleRow(org, host)
	row.RunningDigest = "sha256:run"
	row.Decision = DecisionWait
	row.CandidateCreated = &old
	row.CandidateFirstSeen = &old
	sender := &memSender{}
	st := &memLoopStore{auto: true, updates: map[string][]store.ImageUpdate{DecisionWait: {row}}}
	l := NewLoop(st, sender, nil)
	l.tick(context.Background())
	if len(st.updates[DecisionEligible]) != 1 || len(st.updates[DecisionWait]) != 0 {
		t.Fatalf("wait row not promoted to eligible: %+v", st.updates)
	}
	if n := sender.count(isPull); n != 1 {
		t.Fatalf("pull after promotion = %d, want 1", n)
	}
}

func TestApplyCandidateKeepsFirstSeenForSameDigest(t *testing.T) {
	org, host := uuid.New(), uuid.New()
	now := time.Now()
	earlier := now.Add(-30 * time.Hour)
	c := &agentv1.ImageCandidate{
		ContainerId: "c1", Image: "x", RunningDigest: "sha256:run", CandidateDigest: "sha256:new",
		CandidateCreatedUnixMs: now.Add(-100 * time.Hour).UnixMilli(), Status: "ok",
	}
	prev := &store.ImageUpdate{CandidateDigest: "sha256:new", CandidateFirstSeen: &earlier}
	u := ApplyCandidate(org, host, c, nil, now, "", prev)
	if u.CandidateFirstSeen == nil || !u.CandidateFirstSeen.Equal(earlier) {
		t.Fatalf("first seen not kept: %v", u.CandidateFirstSeen)
	}
	if u.Decision != DecisionWait {
		t.Fatalf("created is old but first seen is 30h ago, want wait, got %s", u.Decision)
	}
	prev.CandidateDigest = "sha256:other"
	u = ApplyCandidate(org, host, c, nil, now, "", prev)
	if u.CandidateFirstSeen == nil || !u.CandidateFirstSeen.Equal(now) {
		t.Fatalf("first seen not reset for new digest: %v", u.CandidateFirstSeen)
	}
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
