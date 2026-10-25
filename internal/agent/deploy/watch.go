package deploy

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alexseymer/haetae/internal/deployspec"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

const (
	defaultWatchInterval  = 10 * time.Second
	lsRemoteTimeout       = 20 * time.Second
	maxLSRemoteConcurrent = 8
)

// Watcher polls watched branches and reports when their heads move.
type Watcher struct {
	Runner        Runner
	AllowLocalGit bool
	Interval      time.Duration
	Log           *slog.Logger

	mu       sync.Mutex
	branches []*agentv1.WatchedBranch
	reported map[string]string // project -> last reported commit
}

// SetWatchSet replaces the set of branches to poll.
func (w *Watcher) SetWatchSet(ws *agentv1.WatchSet) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.branches = nil
	if ws == nil {
		return
	}
	for _, b := range ws.GetBranches() {
		if b == nil {
			continue
		}
		// Clone fields only; do not copy the protobuf message (it embeds a mutex).
		w.branches = append(w.branches, &agentv1.WatchedBranch{
			Project:    b.GetProject(),
			Url:        b.GetUrl(),
			Ref:        b.GetRef(),
			Token:      b.GetToken(),
			LastCommit: b.GetLastCommit(),
		})
	}
}

// Run polls until ctx is cancelled. report is called for each BranchUpdate.
func (w *Watcher) Run(ctx context.Context, report func(*agentv1.BranchUpdate)) {
	interval := w.Interval
	if interval <= 0 {
		interval = defaultWatchInterval
	}
	if w.reported == nil {
		w.reported = make(map[string]string)
	}
	log := w.Log
	if log == nil {
		log = slog.Default()
	}

	t := time.NewTimer(0) // fire immediately once
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.pollOnce(ctx, report, log)
			t.Reset(interval)
		}
	}
}

func (w *Watcher) pollOnce(ctx context.Context, report func(*agentv1.BranchUpdate), log *slog.Logger) {
	w.mu.Lock()
	branches := append([]*agentv1.WatchedBranch(nil), w.branches...)
	w.mu.Unlock()

	sem := make(chan struct{}, maxLSRemoteConcurrent)
	var wg sync.WaitGroup
	for _, b := range branches {
		if b == nil || deployspec.IsCommit(b.GetRef()) {
			continue
		}
		if err := deployspec.ProjectName(b.GetProject()); err != nil {
			continue
		}
		if err := deployspec.GitURL(b.GetUrl(), w.AllowLocalGit); err != nil {
			continue
		}
		if err := deployspec.GitRef(b.GetRef()); err != nil {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(b *agentv1.WatchedBranch) {
			defer wg.Done()
			defer func() { <-sem }()
			commit, err := w.lsRemote(ctx, b)
			if err != nil {
				log.Debug("watch ls-remote failed", "project", b.GetProject(), "err", err)
				return
			}
			if commit == "" {
				return
			}
			w.mu.Lock()
			prev := w.reported[b.GetProject()]
			last := b.GetLastCommit()
			should := false
			switch {
			case prev == commit:
				// already reported
			case last == "":
				should = true
			case commit != last && prev != commit:
				should = true
			}
			if should {
				w.reported[b.GetProject()] = commit
			}
			w.mu.Unlock()
			if should && report != nil {
				report(&agentv1.BranchUpdate{Project: b.GetProject(), Commit: commit})
			}
		}(b)
	}
	wg.Wait()
}

func (w *Watcher) lsRemote(ctx context.Context, b *agentv1.WatchedBranch) (string, error) {
	rctx, cancel := context.WithTimeout(ctx, lsRemoteTimeout)
	defer cancel()

	env, cleanup, err := gitEnv(b.GetToken())
	if err != nil {
		return "", err
	}
	defer cleanup()

	var out strings.Builder
	args := append(gitConfigArgs(w.AllowLocalGit), "ls-remote", "--heads", "--", b.GetUrl(), b.GetRef())
	err = w.Runner.Run(rctx, Cmd{
		Env:  env,
		Name: "git",
		Args: args,
		Stdout: func(line string) {
			if out.Len() == 0 {
				out.WriteString(line)
			}
		},
	})
	if err != nil {
		return "", err
	}
	return parseLSRemote(out.String()), nil
}

func parseLSRemote(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
