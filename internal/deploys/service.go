// Package deploys decides when to start a deploy and talks to the agent
// through the gateway. Persistence and encryption live in store and secrets.
package deploys

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/secrets"
	"github.com/alexseymer/haetae/internal/store"
)

// Sentinel errors returned to the API layer.
var (
	ErrBusy        = errors.New("deploys: a deploy is already running for this project")
	ErrHostOffline = errors.New("deploys: host is not connected")
	ErrNotHealthy  = errors.New("deploys: target deploy is not healthy")
	ErrSameDeploy  = errors.New("deploys: cannot roll back to the current deploy")
	// ErrNotDelivered means the command never reached the agent session outbox
	// (no session, enqueue failed, or session closed before enqueue). Only then
	// may Start mark the deploy failed. A wait/ack timeout after enqueue must
	// not be wrapped as ErrNotDelivered — leave the deploy queued.
	ErrNotDelivered = errors.New("deploys: command not delivered to agent")
)

const (
	sendTimeout        = 10 * time.Second
	removeSendTimeout  = 60 * time.Second // compose down may wait out Docker's 10s stop grace
	pushDeployCooldown = 30 * time.Second
)

// Store is the persistence the service needs.
type Store interface {
	GetProject(ctx context.Context, orgID, id uuid.UUID) (store.Project, error)
	GetProjectByName(ctx context.Context, orgID, hostID uuid.UUID, name string) (store.Project, error)
	ListProjects(ctx context.Context, orgID, hostID uuid.UUID) ([]store.Project, error)
	CreateDeploy(ctx context.Context, p store.Project, trigger string, rollbackOf, startedBy *uuid.UUID) (store.Deploy, error)
	GetDeploy(ctx context.Context, orgID, id uuid.UUID) (store.Deploy, error)
	ListDeploys(ctx context.Context, orgID, projectID uuid.UUID, limit int) ([]store.Deploy, error)
	ApplyDeployEvent(ctx context.Context, orgID, hostID, deployID uuid.UUID, ev store.DeployEvent) error
	MarkDeployFailed(ctx context.Context, orgID, hostID, deployID uuid.UUID, errText string) error
	LastHealthyDeploy(ctx context.Context, orgID, projectID uuid.UUID) (store.Deploy, error)
	RunningDeploy(ctx context.Context, orgID, projectID uuid.UUID) (store.Deploy, error)
}

// Sender delivers commands to a connected agent.
//
// Send must return ErrNotDelivered when the command never reached the session
// outbox. A timeout waiting for the agent's ack after a successful enqueue
// must return a non-ErrNotDelivered error (typically context.DeadlineExceeded).
type Sender interface {
	Connected(hostID uuid.UUID) bool
	Send(ctx context.Context, hostID uuid.UUID, cmd *agentv1.Command) (*agentv1.CommandResult, error)
}

// StartOpts tunes a deploy start; zero values are fine.
type StartOpts struct {
	IsRollback bool
	RollbackOf *uuid.UUID
	// OverrideSource, when set, replaces the project's source on the deploy
	// snapshot (used for rollbacks of a stored version).
	OverrideSource *store.Deploy
}

// Service starts deploys and builds WatchSets.
type Service struct {
	Store  Store
	Sender Sender
	Box    *secrets.Box

	mu       sync.Mutex
	lastPush map[uuid.UUID]time.Time // project ID → last push-triggered Start
	now      func() time.Time        // tests may override; nil → time.Now
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Start creates a deploy and asks the agent to run it.
func (s *Service) Start(ctx context.Context, orgID, hostID uuid.UUID, project store.Project, trigger string, startedBy *uuid.UUID, opts StartOpts) (store.Deploy, error) {
	if _, err := s.Store.RunningDeploy(ctx, orgID, project.ID); err == nil {
		return store.Deploy{}, ErrBusy
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Deploy{}, err
	}
	if s.Sender == nil || !s.Sender.Connected(hostID) {
		return store.Deploy{}, ErrHostOffline
	}

	snap := project
	if opts.OverrideSource != nil {
		snap.SourceKind = opts.OverrideSource.SourceKind
		snap.GitURL = opts.OverrideSource.GitURL
		snap.GitRef = opts.OverrideSource.GitRef
		snap.ComposePath = opts.OverrideSource.ComposePath
		snap.InlineCompose = opts.OverrideSource.InlineCompose
	}

	d, err := s.Store.CreateDeploy(ctx, snap, trigger, opts.RollbackOf, startedBy)
	if err != nil {
		if errors.Is(err, store.ErrActiveDeploy) {
			return store.Deploy{}, ErrBusy
		}
		return store.Deploy{}, err
	}

	cmd, err := s.buildDeployCommand(ctx, orgID, project, d, opts)
	if err != nil {
		_ = s.Store.MarkDeployFailed(ctx, orgID, hostID, d.ID, err.Error())
		d.Status = store.DeployFailed
		d.Error = err.Error()
		return d, err
	}

	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	res, err := s.Sender.Send(sendCtx, hostID, cmd)
	if err != nil {
		// Only mark failed when the command never left the server. If it was
		// already on the session outbox, leave queued and let agent events or
		// ExpireStaleDeploys finish.
		if errors.Is(err, ErrNotDelivered) {
			_ = s.Store.MarkDeployFailed(ctx, orgID, hostID, d.ID, err.Error())
			d.Status = store.DeployFailed
			d.Error = err.Error()
			return d, fmt.Errorf("deploys: send: %w", err)
		}
		return d, nil
	}
	if res != nil && res.GetError() != "" {
		_ = s.Store.MarkDeployFailed(ctx, orgID, hostID, d.ID, res.GetError())
		d.Status = store.DeployFailed
		d.Error = res.GetError()
		return d, fmt.Errorf("deploys: agent refused: %s", res.GetError())
	}
	return d, nil
}

func (s *Service) buildDeployCommand(ctx context.Context, orgID uuid.UUID, project store.Project, d store.Deploy, opts StartOpts) (*agentv1.Command, error) {
	msg := &agentv1.Deploy{
		DeployId:   d.ID.String(),
		Project:    project.Name,
		Domain:     project.Domain,
		Service:    project.Service,
		Port:       uint32(max(0, project.Port)), //nolint:gosec // G115: port validated 1..65535
		IsRollback: opts.IsRollback,
	}
	token, err := s.decryptToken(ctx, orgID, project)
	if err != nil {
		return nil, err
	}
	switch d.SourceKind {
	case "git":
		msg.Source = &agentv1.Deploy_Git{Git: &agentv1.GitSource{
			Url: d.GitURL, Ref: d.GitRef, ComposePath: d.ComposePath, Token: token,
		}}
	case "inline":
		msg.Source = &agentv1.Deploy_Inline{Inline: &agentv1.InlineCompose{Content: d.InlineCompose}}
	default:
		return nil, fmt.Errorf("deploys: unknown source kind %q", d.SourceKind)
	}

	if lh, err := s.Store.LastHealthyDeploy(ctx, orgID, project.ID); err == nil && lh.ID != d.ID {
		// last_healthy git must carry the project's current token (never copied into deploy rows).
		lhToken := ""
		if lh.SourceKind == "git" {
			lhToken = token
		}
		msg.LastHealthy = versionFromDeploy(lh, lhToken)
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	return &agentv1.Command{Kind: &agentv1.Command_Deploy{Deploy: msg}}, nil
}

func (s *Service) decryptToken(ctx context.Context, orgID uuid.UUID, project store.Project) (string, error) {
	full := project
	if len(full.GitTokenSealed) == 0 && (full.HasToken) {
		var err error
		full, err = s.Store.GetProject(ctx, orgID, project.ID)
		if err != nil {
			return "", err
		}
	}
	if len(full.GitTokenSealed) == 0 {
		return "", nil
	}
	if s.Box == nil {
		return "", errors.New("deploys: secrets box not configured")
	}
	plain, err := s.Box.Open(full.GitTokenSealed, project.ID.String())
	if err != nil {
		return "", fmt.Errorf("deploys: open token: %w", err)
	}
	return string(plain), nil
}

func versionFromDeploy(d store.Deploy, gitToken string) *agentv1.Version {
	switch d.SourceKind {
	case "git":
		ref := d.GitRef
		if d.Commit != "" {
			ref = d.Commit
		}
		return &agentv1.Version{Kind: &agentv1.Version_Git{Git: &agentv1.GitSource{
			Url: d.GitURL, Ref: ref, ComposePath: d.ComposePath, Token: gitToken,
		}}}
	case "inline":
		return &agentv1.Version{Kind: &agentv1.Version_Inline{Inline: &agentv1.InlineCompose{Content: d.InlineCompose}}}
	default:
		return nil
	}
}

// Rollback starts a deploy that re-uses toDeployID's stored version.
func (s *Service) Rollback(ctx context.Context, orgID, hostID uuid.UUID, project store.Project, toDeployID uuid.UUID, startedBy *uuid.UUID) (store.Deploy, error) {
	target, err := s.Store.GetDeploy(ctx, orgID, toDeployID)
	if err != nil {
		return store.Deploy{}, err
	}
	if target.ProjectID != project.ID {
		return store.Deploy{}, store.ErrNotFound
	}
	if target.Status != store.DeployHealthy {
		return store.Deploy{}, ErrNotHealthy
	}
	newest, err := s.Store.ListDeploys(ctx, orgID, project.ID, 1)
	if err != nil {
		return store.Deploy{}, err
	}
	if len(newest) == 1 && newest[0].ID == target.ID {
		return store.Deploy{}, ErrSameDeploy
	}

	rb := toDeployID
	return s.Start(ctx, orgID, hostID, project, store.TriggerRollback, startedBy, StartOpts{
		IsRollback:     true,
		RollbackOf:     &rb,
		OverrideSource: &target,
	})
}

// WatchSet builds the auto-deploy watch list for a host.
func (s *Service) WatchSet(ctx context.Context, orgID, hostID uuid.UUID) (*agentv1.WatchSet, error) {
	ps, err := s.Store.ListProjects(ctx, orgID, hostID)
	if err != nil {
		return nil, err
	}
	out := &agentv1.WatchSet{}
	for _, p := range ps {
		if !p.AutoDeploy || p.SourceKind != "git" {
			continue
		}
		token, err := s.decryptToken(ctx, orgID, p)
		if err != nil {
			return nil, err
		}
		lastCommit := ""
		if ds, err := s.Store.ListDeploys(ctx, orgID, p.ID, 1); err != nil {
			return nil, err
		} else if len(ds) == 1 {
			lastCommit = ds[0].Commit
		}
		out.Branches = append(out.Branches, &agentv1.WatchedBranch{
			Project: p.Name, Url: p.GitURL, Ref: p.GitRef, Token: token, LastCommit: lastCommit,
		})
	}
	return out, nil
}

// Remove asks the agent to stop a project's containers (never volumes).
func (s *Service) Remove(ctx context.Context, hostID uuid.UUID, projectName string) error {
	if s.Sender == nil || !s.Sender.Connected(hostID) {
		return ErrHostOffline
	}
	sendCtx, cancel := context.WithTimeout(ctx, removeSendTimeout)
	defer cancel()
	res, err := s.Sender.Send(sendCtx, hostID, &agentv1.Command{
		Kind: &agentv1.Command_RemoveProject{RemoveProject: &agentv1.RemoveProject{Project: projectName}},
	})
	if err != nil {
		return err
	}
	if res != nil && res.GetError() != "" {
		return fmt.Errorf("deploys: agent refused remove: %s", res.GetError())
	}
	return nil
}

// OnBranchUpdate starts a push-triggered deploy when the project is auto-deploy
// git and idle. Otherwise it is a no-op. Ignores updates whose commit matches
// the most recent non-failed deploy, and enforces a per-project cooldown.
func (s *Service) OnBranchUpdate(ctx context.Context, orgID, hostID uuid.UUID, projectName, commit string) error {
	p, err := s.Store.GetProjectByName(ctx, orgID, hostID, projectName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if !p.AutoDeploy || p.SourceKind != "git" {
		return nil
	}
	if commit != "" {
		if same, err := s.sameAsLatestNonFailed(ctx, orgID, p.ID, commit); err != nil {
			return err
		} else if same {
			return nil
		}
	}
	if _, err := s.Store.RunningDeploy(ctx, orgID, p.ID); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if !s.allowPushDeploy(p.ID) {
		return nil
	}
	_, err = s.Start(ctx, orgID, hostID, p, store.TriggerPush, nil, StartOpts{})
	if errors.Is(err, ErrBusy) || errors.Is(err, ErrHostOffline) {
		return nil
	}
	return err
}

func (s *Service) sameAsLatestNonFailed(ctx context.Context, orgID, projectID uuid.UUID, commit string) (bool, error) {
	ds, err := s.Store.ListDeploys(ctx, orgID, projectID, 20)
	if err != nil {
		return false, err
	}
	for _, d := range ds {
		if d.Status == store.DeployFailed {
			continue
		}
		return d.Commit != "" && d.Commit == commit, nil
	}
	return false, nil
}

func (s *Service) allowPushDeploy(projectID uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastPush == nil {
		s.lastPush = make(map[uuid.UUID]time.Time)
	}
	now := s.clock()
	if last, ok := s.lastPush[projectID]; ok && now.Sub(last) < pushDeployCooldown {
		return false
	}
	s.lastPush[projectID] = now
	return true
}
