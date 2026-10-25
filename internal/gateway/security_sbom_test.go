package gateway

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/store"
)

type fakeSbomStore struct {
	mu            sync.Mutex
	belong        map[string]bool
	saved         []store.SaveSbomInput
	failed        []store.FailedScanInput
	rejectForeign bool
}

func (f *fakeSbomStore) ContainerBelongsToHost(_ context.Context, _, _ uuid.UUID, containerID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if containerID == "" {
		return true, nil
	}
	if f.belong != nil {
		return f.belong[containerID], nil
	}
	return !f.rejectForeign, nil
}

func (f *fakeSbomStore) SaveSbom(_ context.Context, _ uuid.UUID, in store.SaveSbomInput) (store.Scan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.ContainerID != "" && f.belong != nil && !f.belong[in.ContainerID] {
		return store.Scan{}, store.ErrForeignContainer
	}
	f.saved = append(f.saved, in)
	return store.Scan{ID: uuid.New(), Status: store.ScanOK, ImageID: in.ImageID}, nil
}

func (f *fakeSbomStore) RecordFailedScan(_ context.Context, _ uuid.UUID, in store.FailedScanInput) (store.Scan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, in)
	return store.Scan{Status: in.Status}, nil
}

func (f *fakeSbomStore) ReplaceFindings(context.Context, uuid.UUID, string, []store.Finding) error {
	return nil
}
func (f *fakeSbomStore) SetMatchState(context.Context, uuid.UUID, string, string, string, *time.Time) error {
	return nil
}

type fakeMatchQueue struct {
	ready bool
	n     int
}

func (q *fakeMatchQueue) EnqueueMatch(uuid.UUID, string) { q.n++ }
func (q *fakeMatchQueue) Ready() bool                    { return q.ready }

func TestHandleSbomReportRejectsForeignContainer(t *testing.T) {
	g := New(nil, nil, Config{}, slog.Default())
	st := &fakeSbomStore{belong: map[string]bool{"mine": true}}
	g.SetSbomHandlers(st, &fakeMatchQueue{})
	s := &session{hostID: uuid.New(), orgID: uuid.New()}
	g.handleSbomReport(context.Background(), s, &agentv1.SbomReport{
		ContainerId: "other", ImageId: "sha", Status: "ok", Trigger: "start",
		Packages: []*agentv1.SbomPackage{{Name: "p", Version: "1"}},
	})
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.saved) != 0 {
		t.Fatalf("saved foreign report: %+v", st.saved)
	}
}

func TestHandleSbomReportRejectsOversize(t *testing.T) {
	g := New(nil, nil, Config{}, slog.Default())
	st := &fakeSbomStore{}
	g.SetSbomHandlers(st, &fakeMatchQueue{})
	s := &session{hostID: uuid.New(), orgID: uuid.New()}
	pkgs := make([]*agentv1.SbomPackage, store.MaxSbomPackages+1)
	for i := range pkgs {
		pkgs[i] = &agentv1.SbomPackage{Name: "p", Version: "1"}
	}
	g.handleSbomReport(context.Background(), s, &agentv1.SbomReport{
		ImageId: "sha", Status: "ok", Trigger: "start", Packages: pkgs,
	})
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.saved) != 0 {
		t.Fatal("saved oversize report")
	}
	if len(st.failed) != 1 || st.failed[0].Status != store.ScanFailed {
		t.Fatalf("failed = %+v", st.failed)
	}
}

func TestHandleSbomReportOK(t *testing.T) {
	g := New(nil, nil, Config{}, slog.Default())
	st := &fakeSbomStore{}
	q := &fakeMatchQueue{ready: true}
	g.SetSbomHandlers(st, q)
	s := &session{hostID: uuid.New(), orgID: uuid.New()}
	g.handleSbomReport(context.Background(), s, &agentv1.SbomReport{
		ImageId: "sha256:x", Status: "ok", Trigger: "deploy",
		DistroName: "alpine", DistroVersion: "3.20",
		Packages: []*agentv1.SbomPackage{{Name: "libfoo", Version: "1.0.0", Type: "apk"}},
	})
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.saved) != 1 {
		t.Fatalf("saved = %d", len(st.saved))
	}
	if q.n != 1 {
		t.Fatalf("match enqueue = %d", q.n)
	}
}
