package gateway

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

type memSecurityStore struct {
	misconfigs []store.Misconfig
	hostSec    *store.HostSecurity
	updates    []store.ImageUpdate
	prev       *store.ImageUpdate
}

func (m *memSecurityStore) GetImageUpdate(context.Context, uuid.UUID, uuid.UUID, string) (store.ImageUpdate, error) {
	if m.prev == nil {
		return store.ImageUpdate{}, store.ErrNotFound
	}
	return *m.prev, nil
}

func (m *memSecurityStore) ReplaceMisconfigs(_ context.Context, _, _ uuid.UUID, ms []store.Misconfig) error {
	m.misconfigs = append([]store.Misconfig(nil), ms...)
	return nil
}
func (m *memSecurityStore) UpsertHostSecurity(_ context.Context, hs store.HostSecurity) error {
	cp := hs
	m.hostSec = &cp
	return nil
}
func (m *memSecurityStore) UpsertOutbound(context.Context, uuid.UUID, uuid.UUID, []store.OutboundConn) error {
	return nil
}
func (m *memSecurityStore) UpsertImageUpdate(_ context.Context, u store.ImageUpdate) error {
	m.updates = append(m.updates, u)
	return nil
}
func (m *memSecurityStore) ContainerBelongs(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	return true, nil
}

func TestHandleSecurityReportClipsAndStores(t *testing.T) {
	sec := &memSecurityStore{}
	g := New(securityStoreAdapter{sec}, nil, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := newSession(uuid.New(), func(error) {})
	s.orgID = uuid.New()

	detail := make([]byte, 400)
	for i := range detail {
		detail[i] = 'x'
	}
	g.handleSecurityReport(context.Background(), s, &agentv1.SecurityReport{
		Misconfigs: []*agentv1.Misconfig{
			{ContainerId: "c1", Kind: "privileged", Severity: "high", Detail: string(detail)},
			{Kind: "not_a_kind", Severity: "high", Detail: "x"},
		},
		Conntrack: &agentv1.ConntrackStatus{State: "ok", Reason: "fine"},
	})
	if len(sec.misconfigs) != 1 {
		t.Fatalf("misconfigs=%d", len(sec.misconfigs))
	}
	if len(sec.misconfigs[0].Detail) > maxDetailLen {
		t.Fatalf("detail not clipped: %d", len(sec.misconfigs[0].Detail))
	}
	if sec.hostSec == nil || sec.hostSec.ConntrackState != "ok" {
		t.Fatalf("hostSec=%v", sec.hostSec)
	}
}

func candidateReport() *agentv1.ImageCandidates {
	old := time.Now().Add(-72 * time.Hour).UnixMilli()
	return &agentv1.ImageCandidates{
		Candidates: []*agentv1.ImageCandidate{{
			ContainerId:            "c1",
			Image:                  "busybox:latest",
			RunningDigest:          "sha256:aaa",
			CandidateDigest:        "sha256:bbb",
			CandidateCreatedUnixMs: old,
			Status:                 "ok",
		}},
	}
}

// A digest the server has just seen is young whatever "created" claims.
func TestHandleImageCandidatesFirstSightWaits(t *testing.T) {
	sec := &memSecurityStore{}
	g := New(securityStoreAdapter{sec}, nil, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := newSession(uuid.New(), func(error) {})
	s.orgID = uuid.New()
	g.handleImageCandidates(context.Background(), s, candidateReport())
	if len(sec.updates) != 1 || sec.updates[0].Decision != "wait" {
		t.Fatalf("updates=%+v", sec.updates)
	}
}

func TestHandleImageCandidatesDecides(t *testing.T) {
	sec := &memSecurityStore{}
	seen := time.Now().Add(-72 * time.Hour)
	sec.prev = &store.ImageUpdate{CandidateDigest: "sha256:bbb", CandidateFirstSeen: &seen}
	g := New(securityStoreAdapter{sec}, nil, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := newSession(uuid.New(), func(error) {})
	s.orgID = uuid.New()
	g.handleImageCandidates(context.Background(), s, candidateReport())
	if len(sec.updates) != 1 {
		t.Fatalf("updates=%d", len(sec.updates))
	}
	if sec.updates[0].Decision != "eligible" {
		t.Fatalf("decision=%s reason=%s", sec.updates[0].Decision, sec.updates[0].Reason)
	}
}

func (a securityStoreAdapter) GetImageUpdate(ctx context.Context, o, h uuid.UUID, c string) (store.ImageUpdate, error) {
	return a.securityStore.(*memSecurityStore).GetImageUpdate(ctx, o, h, c)
}

type securityStoreAdapter struct{ securityStore }

func (securityStoreAdapter) EnrollHost(context.Context, string, store.HostInfo, func(uuid.UUID) (string, error)) (store.Host, error) {
	panic("unused")
}
func (securityStoreAdapter) HostForAgent(context.Context, uuid.UUID) (store.Host, error) {
	panic("unused")
}
func (securityStoreAdapter) MarkConnected(context.Context, uuid.UUID, store.HostInfo, time.Time) error {
	panic("unused")
}
func (securityStoreAdapter) MarkSeen(context.Context, uuid.UUID, time.Time) error { panic("unused") }
func (securityStoreAdapter) MarkDisconnected(context.Context, uuid.UUID) error    { panic("unused") }
func (securityStoreAdapter) ApplyDeployEvent(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, store.DeployEvent) error {
	panic("unused")
}
func (securityStoreAdapter) AppendDeployLog(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, store.DeployLogChunk) error {
	panic("unused")
}
func (securityStoreAdapter) ReplaceContainers(context.Context, uuid.UUID, uuid.UUID, []store.Container, bool) error {
	panic("unused")
}
func (securityStoreAdapter) InsertHostMetrics(context.Context, uuid.UUID, uuid.UUID, []store.HostMetric) error {
	panic("unused")
}
func (securityStoreAdapter) InsertContainerMetrics(context.Context, uuid.UUID, uuid.UUID, []store.ContainerMetric) error {
	panic("unused")
}
func (securityStoreAdapter) InsertLogEvents(context.Context, uuid.UUID, uuid.UUID, []store.LogEvent) error {
	panic("unused")
}
func (securityStoreAdapter) UpsertTemplateCandidates(context.Context, uuid.UUID, uuid.UUID, []store.LogTemplate) error {
	panic("unused")
}
func (securityStoreAdapter) SetLogStats(context.Context, uuid.UUID, uuid.UUID, store.LogStats) error {
	panic("unused")
}
func (securityStoreAdapter) JudgeableTemplates(context.Context, uuid.UUID, uuid.UUID, int64, int, int) ([]store.LogTemplate, error) {
	panic("unused")
}
func (securityStoreAdapter) SetVerdicts(context.Context, uuid.UUID, uuid.UUID, []string, []string) error {
	panic("unused")
}
func (securityStoreAdapter) GreenTemplateIDs(context.Context, uuid.UUID, uuid.UUID, int) ([]string, error) {
	panic("unused")
}
