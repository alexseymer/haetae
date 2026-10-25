package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alexseymer/haetae/internal/deployphase"
	"github.com/alexseymer/haetae/internal/deployspec"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/redact"
)

const (
	defaultHealthWindow = 10 * time.Second
	defaultStartTimeout = 180 * time.Second
	defaultBuildTimeout = 15 * time.Minute
	logBatchLines       = 50
	logBatchWait        = 500 * time.Millisecond
	maxDeployLogBytes   = 2 << 20
	healthPollInterval  = 2 * time.Second
	stateFileMode       = 0o600
	composeFileMode     = 0o600
)

// Ingress routes a project to a domain. Implemented by internal/agent/ingress.
type Ingress interface {
	Route(ctx context.Context, project, domain, upstream string) (url string, err error)
	Unroute(ctx context.Context, project string) error
}

// Emitter sends deploy progress. Implemented by the agent outbox.
type Emitter interface {
	Event(*agentv1.DeployEvent)
	Log(*agentv1.DeployLog)
}

// Config tunes the deploy manager. Zero durations get defaults.
type Config struct {
	StateDir      string
	AllowLocalGit bool
	Runner        Runner
	Ingress       Ingress
	Emit          Emitter
	Redact        func(ctx context.Context, lines []string) ([]string, error)
	Now           func() time.Time
	HealthWindow  time.Duration
	StartTimeout  time.Duration
	BuildTimeout  time.Duration
}

func (c Config) withDefaults() Config {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.HealthWindow <= 0 {
		c.HealthWindow = defaultHealthWindow
	}
	if c.StartTimeout <= 0 {
		c.StartTimeout = defaultStartTimeout
	}
	if c.BuildTimeout <= 0 {
		c.BuildTimeout = defaultBuildTimeout
	}
	if c.Redact == nil {
		c.Redact = func(context.Context, []string) ([]string, error) {
			return nil, errors.New("deploy: redactor not configured")
		}
	}
	return c
}

// Manager runs deploys for projects. At most one deploy runs per project.
type Manager struct {
	cfg    Config
	mu     sync.Mutex
	active map[string]context.CancelFunc
	wg     sync.WaitGroup
	closed bool
	cancel context.CancelFunc
	ctx    context.Context
}

// New returns a Manager. Call Close to cancel in-flight deploys.
func New(cfg Config) *Manager {
	cfg = cfg.withDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:    cfg,
		active: make(map[string]context.CancelFunc),
		ctx:    ctx,
		cancel: cancel,
	}
}

// Wait blocks until every in-flight deploy finishes (for tests).
func (m *Manager) Wait() { m.wg.Wait() }

// Close cancels all deploys and waits for them to finish.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
}

// Handle validates d and starts the deploy in the background. It returns an
// error only for validation or "already running"; an accepted deploy returns nil.
func (m *Manager) Handle(ctx context.Context, d *agentv1.Deploy) error {
	if d == nil {
		return errors.New("deploy is required")
	}
	if err := validateDeploy(d, m.cfg.AllowLocalGit); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("agent stopped")
	}
	if _, busy := m.active[d.GetProject()]; busy {
		return fmt.Errorf("deploy already running for project %s", d.GetProject())
	}
	dctx, cancel := context.WithCancel(m.ctx)
	m.active[d.GetProject()] = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.active, d.GetProject())
			m.mu.Unlock()
			cancel()
		}()
		m.runDeploy(dctx, d)
	}()
	return nil
}

// Remove stops a project's containers, unroutes ingress, and deletes its source
// directory. Volumes and state.json are kept.
func (m *Manager) Remove(ctx context.Context, project string) error {
	if err := deployspec.ProjectName(project); err != nil {
		return err
	}
	m.mu.Lock()
	if _, busy := m.active[project]; busy {
		m.mu.Unlock()
		return fmt.Errorf("deploy already running for project %s", project)
	}
	m.mu.Unlock()

	cmd := Cmd{
		Name: "docker",
		Args: []string{"compose", "-p", project, "down", "--remove-orphans"},
	}
	if err := m.cfg.Runner.Run(ctx, cmd); err != nil {
		return fmt.Errorf("compose down: %w", err)
	}
	if m.cfg.Ingress != nil {
		if err := m.cfg.Ingress.Unroute(ctx, project); err != nil {
			return fmt.Errorf("unroute: %w", err)
		}
	}
	projDir := m.projectDir(project)
	srcDir := filepath.Join(projDir, "src")
	_ = os.RemoveAll(srcDir)
	_ = os.Remove(filepath.Join(projDir, "compose.yaml"))
	return nil
}

func (m *Manager) projectDir(name string) string {
	return filepath.Join(m.cfg.StateDir, "projects", name)
}

func validateDeploy(d *agentv1.Deploy, allowLocalGit bool) error {
	if err := deployspec.ProjectName(d.GetProject()); err != nil {
		return err
	}
	if d.GetDeployId() == "" {
		return errors.New("deploy id is required")
	}
	if err := validateSource(d.GetGit(), d.GetInline(), allowLocalGit); err != nil {
		return err
	}
	if err := deployspec.Domain(d.GetDomain()); err != nil {
		return err
	}
	if d.GetDomain() != "" {
		if err := deployspec.Service(d.GetService()); err != nil {
			return err
		}
		if err := deployspec.Port(d.GetPort()); err != nil {
			return err
		}
	}
	if lh := d.GetLastHealthy(); lh != nil {
		if err := validateVersion(lh, allowLocalGit); err != nil {
			return fmt.Errorf("last_healthy: %w", err)
		}
	}
	return nil
}

func validateVersion(v *agentv1.Version, allowLocalGit bool) error {
	if v == nil {
		return errors.New("version is required")
	}
	return validateSource(v.GetGit(), v.GetInline(), allowLocalGit)
}

func validateSource(git *agentv1.GitSource, inline *agentv1.InlineCompose, allowLocalGit bool) error {
	hasGit := git != nil
	hasInline := inline != nil
	switch {
	case hasGit && hasInline:
		return errors.New("source must be either git or inline compose, not both")
	case !hasGit && !hasInline:
		return errors.New("source must be either git or inline compose")
	case hasGit:
		if err := deployspec.GitURL(git.GetUrl(), allowLocalGit); err != nil {
			return err
		}
		if err := deployspec.GitRef(git.GetRef()); err != nil {
			return err
		}
		if _, err := deployspec.ComposePath(git.GetComposePath()); err != nil {
			return err
		}
		return nil
	default:
		return deployspec.InlineCompose(inline.GetContent())
	}
}

type deployState struct {
	DeployID string `json:"deploy_id"`
	Phase    string `json:"phase"`
	Commit   string `json:"commit,omitempty"`
	Time     string `json:"time"`
}

func (m *Manager) persistState(project, deployID string, phase agentv1.DeployPhase, commit string) {
	dir := m.projectDir(project)
	_ = os.MkdirAll(dir, 0o700)
	st := deployState{
		DeployID: deployID,
		Phase:    phase.String(),
		Commit:   commit,
		Time:     m.cfg.Now().UTC().Format(time.RFC3339Nano),
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "state.json"), b, stateFileMode)
}

type logStreamer struct {
	m        *Manager
	deployID string
	pem      redact.PEMGuard
	mu       sync.Mutex
	seq      uint32
	bytes    int
	trunc    bool
	bufs     map[string]*lineBuf // stream -> buffer
}

type lineBuf struct {
	lines []string
	timer *time.Timer
}

func (m *Manager) newLogStreamer(deployID string) *logStreamer {
	return &logStreamer{
		m:        m,
		deployID: deployID,
		bufs:     map[string]*lineBuf{"stdout": {}, "stderr": {}},
	}
}

func (s *logStreamer) line(stream, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.trunc {
		return
	}
	text = s.pem.Filter(text)
	b := s.bufs[stream]
	if b == nil {
		b = &lineBuf{}
		s.bufs[stream] = b
	}
	b.lines = append(b.lines, text)
	if len(b.lines) >= logBatchLines {
		s.flushLocked(stream)
		return
	}
	if b.timer == nil {
		b.timer = time.AfterFunc(logBatchWait, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.flushLocked(stream)
		})
	}
}

func (s *logStreamer) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushLocked("stdout")
	s.flushLocked("stderr")
}

func (s *logStreamer) flushLocked(stream string) {
	b := s.bufs[stream]
	if b == nil || len(b.lines) == 0 {
		return
	}
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	lines := b.lines
	b.lines = nil
	if s.trunc {
		return
	}
	s.emitChunkLocked(stream, lines)
}

func (s *logStreamer) emitChunkLocked(stream string, lines []string) {
	if s.trunc || s.m.cfg.Emit == nil {
		return
	}
	text := strings.Join(lines, "\n")
	if text != "" {
		text += "\n"
	}
	redacted, err := s.m.cfg.Redact(context.Background(), []string{text})
	s.seq++
	chunk := &agentv1.DeployLog{
		DeployId: s.deployID,
		Seq:      s.seq,
		Stream:   stream,
	}
	if err != nil {
		chunk.Withheld = true
	} else if len(redacted) > 0 {
		chunk.Text = redacted[0]
	}
	n := len(chunk.Text)
	if chunk.Withheld {
		n = 0
	}
	if s.bytes+n > maxDeployLogBytes {
		s.trunc = true
		s.seq++
		s.m.cfg.Emit.Log(&agentv1.DeployLog{
			DeployId: s.deployID,
			Seq:      s.seq,
			Stream:   stream,
			Text:     "log truncated\n",
		})
		return
	}
	s.bytes += n
	s.m.cfg.Emit.Log(chunk)
}

// emitEventTracked emits a phase event. When last is non-nil it enforces the
// shared deployphase table (same-phase updates are allowed; illegal jumps are
// dropped so the agent never emits an illegal sequence).
func (m *Manager) emitEventTracked(d *agentv1.Deploy, last *string, phase agentv1.DeployPhase, msg, commit, url, errText string) {
	name := phaseString(phase)
	if last != nil && name != "" {
		if *last != "" && name != *last && !deployphase.Allowed(*last, name) {
			return
		}
		if name != *last {
			*last = name
		}
	}
	ev := &agentv1.DeployEvent{
		DeployId: d.GetDeployId(),
		Phase:    phase,
		Commit:   commit,
		Url:      url,
		TsUnixMs: m.cfg.Now().UnixMilli(),
	}
	ev.Message = m.redactText(msg)
	ev.Error = m.redactText(errText)
	m.persistState(d.GetProject(), d.GetDeployId(), phase, commit)
	if m.cfg.Emit != nil {
		m.cfg.Emit.Event(ev)
	}
}

func phaseString(p agentv1.DeployPhase) string {
	switch p {
	case agentv1.DeployPhase_DEPLOY_PHASE_QUEUED:
		return deployphase.Queued
	case agentv1.DeployPhase_DEPLOY_PHASE_FETCHING:
		return deployphase.Fetching
	case agentv1.DeployPhase_DEPLOY_PHASE_BUILDING:
		return deployphase.Building
	case agentv1.DeployPhase_DEPLOY_PHASE_STARTING:
		return deployphase.Starting
	case agentv1.DeployPhase_DEPLOY_PHASE_CHECKING:
		return deployphase.Checking
	case agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY:
		return deployphase.Healthy
	case agentv1.DeployPhase_DEPLOY_PHASE_FAILED:
		return deployphase.Failed
	case agentv1.DeployPhase_DEPLOY_PHASE_ROLLING_BACK:
		return deployphase.RollingBack
	case agentv1.DeployPhase_DEPLOY_PHASE_ROLLED_BACK:
		return deployphase.RolledBack
	case agentv1.DeployPhase_DEPLOY_PHASE_ROLLBACK_FAILED:
		return deployphase.RollbackFailed
	default:
		return ""
	}
}

func (m *Manager) redactText(s string) string {
	if s == "" {
		return ""
	}
	out, err := m.cfg.Redact(context.Background(), []string{s})
	if err != nil || len(out) == 0 {
		return "[withheld]"
	}
	return out[0]
}

func (m *Manager) runDeploy(ctx context.Context, d *agentv1.Deploy) {
	logs := m.newLogStreamer(d.GetDeployId())
	defer logs.flush()

	last := deployphase.Queued
	url, msg, commit, err := m.deployOnce(ctx, d, logs, false, &last)
	if err == nil {
		if msg == "" {
			msg = "healthy"
		}
		m.emitEventTracked(d, &last, agentv1.DeployPhase_DEPLOY_PHASE_HEALTHY, msg, commit, url, "")
		return
	}
	if ctx.Err() != nil {
		m.emitEventTracked(d, &last, agentv1.DeployPhase_DEPLOY_PHASE_FAILED, "agent stopped", commit, "", "agent stopped")
		return
	}
	if isBuildFailure(err) {
		m.emitEventTracked(d, &last, agentv1.DeployPhase_DEPLOY_PHASE_FAILED, "previous version is still running", commit, "", err.Error())
		return
	}
	if isIngressFailure(err) {
		m.emitEventTracked(d, &last, agentv1.DeployPhase_DEPLOY_PHASE_FAILED, "app is running but not reachable via ingress", commit, "", err.Error())
		return
	}
	// Runtime failure (start/check): rollback when possible.
	if d.GetIsRollback() {
		m.emitEventTracked(d, &last, agentv1.DeployPhase_DEPLOY_PHASE_FAILED, err.Error(), commit, "", err.Error())
		return
	}
	if d.GetLastHealthy() == nil {
		m.emitEventTracked(d, &last, agentv1.DeployPhase_DEPLOY_PHASE_FAILED,
			"no last healthy version to roll back to; containers left for inspection (compose down was not run)",
			commit, "", err.Error())
		return
	}
	m.emitEventTracked(d, &last, agentv1.DeployPhase_DEPLOY_PHASE_ROLLING_BACK, "restoring last healthy version", commit, "", "")
	rb := rollbackDeploy(d)
	rbLogs := m.newLogStreamer(d.GetDeployId())
	defer rbLogs.flush()
	// Rollback must not re-emit fetching/building/… (illegal after rolling_back).
	rbURL, rbMsg, rbCommit, rbErr := m.deployOnce(ctx, rb, rbLogs, true, nil)
	if rbErr != nil {
		msg := rbErr.Error()
		if ctx.Err() != nil {
			msg = "agent stopped"
		}
		m.emitEventTracked(d, &last, agentv1.DeployPhase_DEPLOY_PHASE_ROLLBACK_FAILED, msg, rbCommit, "", msg)
		return
	}
	if rbMsg == "" {
		rbMsg = "rolled back"
	}
	m.emitEventTracked(d, &last, agentv1.DeployPhase_DEPLOY_PHASE_ROLLED_BACK, rbMsg, rbCommit, rbURL, "")
}

type classifiedError struct {
	kind string // "build", "ingress", "runtime"
	msg  string
}

func (e *classifiedError) Error() string { return e.msg }

func buildErr(msg string) error   { return &classifiedError{kind: "build", msg: msg} }
func ingressErr(msg string) error { return &classifiedError{kind: "ingress", msg: msg} }
func runtimeErr(msg string) error { return &classifiedError{kind: "runtime", msg: msg} }

func isBuildFailure(err error) bool {
	var c *classifiedError
	return errors.As(err, &c) && c.kind == "build"
}
func isIngressFailure(err error) bool {
	var c *classifiedError
	return errors.As(err, &c) && c.kind == "ingress"
}

func rollbackDeploy(d *agentv1.Deploy) *agentv1.Deploy {
	rb := &agentv1.Deploy{
		DeployId:   d.GetDeployId(),
		Project:    d.GetProject(),
		Domain:     d.GetDomain(),
		Service:    d.GetService(),
		Port:       d.GetPort(),
		IsRollback: true,
	}
	v := d.GetLastHealthy()
	switch {
	case v.GetGit() != nil:
		rb.Source = &agentv1.Deploy_Git{Git: v.GetGit()}
	case v.GetInline() != nil:
		rb.Source = &agentv1.Deploy_Inline{Inline: v.GetInline()}
	}
	return rb
}

// deployOnce runs fetch → build → start → check. On success it returns the
// public URL (may be empty), a human message, and the resolved commit.
// When last is nil (rollback pass), intermediate phase events are not emitted.
func (m *Manager) deployOnce(ctx context.Context, d *agentv1.Deploy, logs *logStreamer, silentFetch bool, last *string) (url, msg, commit string, err error) {
	emit := func(phase agentv1.DeployPhase, message, cmt, u, errText string) {
		if last == nil {
			return
		}
		m.emitEventTracked(d, last, phase, message, cmt, u, errText)
	}
	if !silentFetch {
		emit(agentv1.DeployPhase_DEPLOY_PHASE_FETCHING, "fetching source", "", "", "")
	} else if last != nil {
		emit(agentv1.DeployPhase_DEPLOY_PHASE_FETCHING, "fetching last healthy source", "", "", "")
	}
	composeFile, commit, err := m.fetch(ctx, d, logs, last)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", commit, err
		}
		// Fetch failures never roll back: containers were not replaced.
		return "", "", commit, buildErr(err.Error())
	}

	emit(agentv1.DeployPhase_DEPLOY_PHASE_BUILDING, "building", commit, "", "")
	if err := m.build(ctx, d, composeFile, logs); err != nil {
		if ctx.Err() != nil {
			return "", "", commit, err
		}
		return "", "", commit, buildErr(err.Error())
	}

	emit(agentv1.DeployPhase_DEPLOY_PHASE_STARTING, "starting", commit, "", "")
	if err := m.start(ctx, d, composeFile, logs); err != nil {
		if ctx.Err() != nil {
			return "", "", commit, err
		}
		return "", "", commit, runtimeErr(err.Error())
	}

	emit(agentv1.DeployPhase_DEPLOY_PHASE_CHECKING, "checking health", commit, "", "")
	url, msg, err = m.checkHealth(ctx, d, composeFile)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", commit, err
		}
		if isIngressFailure(err) {
			return "", "", commit, err
		}
		return "", "", commit, runtimeErr(err.Error())
	}
	return url, msg, commit, nil
}

func (m *Manager) fetch(ctx context.Context, d *agentv1.Deploy, logs *logStreamer, last *string) (composeFile, commit string, err error) {
	projDir := m.projectDir(d.GetProject())
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		return "", "", err
	}
	if inline := d.GetInline(); inline != nil {
		composeFile = filepath.Join(projDir, "compose.yaml")
		if err := os.WriteFile(composeFile, []byte(inline.GetContent()), composeFileMode); err != nil {
			return "", "", err
		}
		return composeFile, "", nil
	}
	git := d.GetGit()
	srcDir := filepath.Join(projDir, "src")
	composePath, err := deployspec.ComposePath(git.GetComposePath())
	if err != nil {
		return "", "", err
	}
	if err := m.gitFetch(ctx, srcDir, git, logs); err != nil {
		return "", "", err
	}
	commit, err = m.gitRevParse(ctx, srcDir)
	if err != nil {
		return "", "", err
	}
	if last != nil {
		m.emitEventTracked(d, last, agentv1.DeployPhase_DEPLOY_PHASE_FETCHING, "fetched "+commit, commit, "", "")
	}
	composeFile = filepath.Join(srcDir, filepath.FromSlash(composePath))
	if err := assertComposeInside(srcDir, composeFile); err != nil {
		return "", commit, err
	}
	return composeFile, commit, nil
}

func assertComposeInside(srcDir, composeFile string) error {
	absSrc, err := filepath.Abs(srcDir)
	if err != nil {
		return err
	}
	absFile, err := filepath.Abs(composeFile)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(absFile)
	if err != nil {
		// If the file does not exist yet, still refuse a path outside src.
		if !strings.HasPrefix(absFile, absSrc+string(os.PathSeparator)) && absFile != absSrc {
			return errors.New("compose file path escapes the source directory")
		}
		return fmt.Errorf("compose file: %w", err)
	}
	resolvedSrc, err := filepath.EvalSymlinks(absSrc)
	if err != nil {
		resolvedSrc = absSrc
	}
	rel, err := filepath.Rel(resolvedSrc, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("compose file resolves outside the source directory (symlink refused)")
	}
	return nil
}

func gitEnv(token string) (env []string, cleanup func(), err error) {
	env = []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	cleanup = func() {}
	if token == "" {
		return env, cleanup, nil
	}
	dir, err := os.MkdirTemp("", "haetae-git-*")
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: temp dir must be owner-executable for askpass
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	askpassPath := filepath.Join(dir, "askpass")
	// Token lives only in the 0600 file; the script cats it (never embeds it).
	script := "#!/bin/sh\nTOKEN_FILE=$(dirname \"$0\")/token\ncase \"$1\" in\n*Username*|*username*) echo x-access-token ;;\n*) cat \"$TOKEN_FILE\" ;;\nesac\n"
	if err := os.WriteFile(askpassPath, []byte(script), 0o700); err != nil { //nolint:gosec // G306: askpass must be executable (0700)
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	env = append(env,
		"GIT_ASKPASS="+askpassPath,
		"GIT_ASKPASS_REQUIRE=force",
	)
	cleanup = func() { _ = os.RemoveAll(dir) }
	return env, cleanup, nil
}

func gitConfigArgs(allowLocal bool) []string {
	args := []string{
		"-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always",
		"-c", "core.hooksPath=/dev/null",
	}
	if allowLocal {
		args = append(args, "-c", "protocol.file.allow=always")
	}
	return args
}

func (m *Manager) runGit(ctx context.Context, dir string, env []string, args []string, logs *logStreamer) error {
	full := append(gitConfigArgs(m.cfg.AllowLocalGit), args...)
	cmd := Cmd{
		Dir:  dir,
		Env:  env,
		Name: "git",
		Args: full,
	}
	if logs != nil {
		cmd.Stdout = func(line string) { logs.line("stdout", line) }
		cmd.Stderr = func(line string) { logs.line("stderr", line) }
	}
	return m.cfg.Runner.Run(ctx, cmd)
}

func (m *Manager) gitFetch(ctx context.Context, srcDir string, git *agentv1.GitSource, logs *logStreamer) error {
	env, cleanup, err := gitEnv(git.GetToken())
	if err != nil {
		return err
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(srcDir, ".git")); err != nil {
		if err := os.MkdirAll(srcDir, 0o700); err != nil {
			return err
		}
		if err := m.runGit(ctx, srcDir, env, []string{"init"}, logs); err != nil {
			return fmt.Errorf("git init: %w", err)
		}
		if err := m.runGit(ctx, srcDir, env, []string{"remote", "add", "origin", "--", git.GetUrl()}, logs); err != nil {
			return fmt.Errorf("git remote add: %w", err)
		}
	}
	ref := git.GetRef()
	if err := m.runGit(ctx, srcDir, env, []string{"fetch", "--depth", "1", "origin", "--", ref}, logs); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	if err := m.runGit(ctx, srcDir, env, []string{"checkout", "--force", "--detach", "FETCH_HEAD"}, logs); err != nil {
		return fmt.Errorf("git checkout: %w", err)
	}
	if err := m.runGit(ctx, srcDir, env, []string{"clean", "-fdx"}, logs); err != nil {
		return fmt.Errorf("git clean: %w", err)
	}
	return nil
}

func (m *Manager) gitRevParse(ctx context.Context, srcDir string) (string, error) {
	var out strings.Builder
	err := m.cfg.Runner.Run(ctx, Cmd{
		Dir:  srcDir,
		Env:  []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"},
		Name: "git",
		Args: append(gitConfigArgs(m.cfg.AllowLocalGit), "rev-parse", "HEAD"),
		Stdout: func(line string) {
			if out.Len() == 0 {
				out.WriteString(strings.TrimSpace(line))
			}
		},
	})
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	commit := out.String()
	if commit == "" {
		return "", errors.New("git rev-parse returned empty commit")
	}
	return commit, nil
}

func (m *Manager) build(ctx context.Context, d *agentv1.Deploy, composeFile string, logs *logStreamer) error {
	bctx, cancel := context.WithTimeout(ctx, m.cfg.BuildTimeout)
	defer cancel()
	var last []string
	record := func(stream, line string) {
		logs.line(stream, line)
		last = append(last, line)
		if len(last) > 20 {
			last = last[len(last)-20:]
		}
	}
	err := m.cfg.Runner.Run(bctx, Cmd{
		Name:   "docker",
		Args:   []string{"compose", "-p", d.GetProject(), "-f", composeFile, "build", "--pull"},
		Stdout: func(line string) { record("stdout", line) },
		Stderr: func(line string) { record("stderr", line) },
	})
	if err != nil {
		msg := strings.Join(last, "\n")
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

func (m *Manager) start(ctx context.Context, d *agentv1.Deploy, composeFile string, logs *logStreamer) error {
	secs := int(m.cfg.StartTimeout / time.Second)
	if secs < 1 {
		secs = 1
	}
	return m.cfg.Runner.Run(ctx, Cmd{
		Name: "docker",
		Args: []string{
			"compose", "-p", d.GetProject(), "-f", composeFile,
			"up", "-d", "--remove-orphans", "--wait", "--wait-timeout", fmt.Sprintf("%d", secs),
		},
		Stdout: func(line string) { logs.line("stdout", line) },
		Stderr: func(line string) { logs.line("stderr", line) },
	})
}

type composePS struct {
	Name       string  `json:"Name"`
	Service    string  `json:"Service"`
	State      string  `json:"State"`
	Health     string  `json:"Health"`
	Publishers []psPub `json:"Publishers"`
	ExitCode   int     `json:"ExitCode"`
}

type psPub struct {
	URL           string `json:"URL"`
	TargetPort    int    `json:"TargetPort"`
	PublishedPort int    `json:"PublishedPort"`
	Protocol      string `json:"Protocol"`
}

func (m *Manager) checkHealth(ctx context.Context, d *agentv1.Deploy, composeFile string) (url, msg string, err error) {
	deadline := m.cfg.Now().Add(m.cfg.HealthWindow)
	var last []composePS
	for {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		services, err := m.composePS(ctx, d.GetProject(), composeFile)
		if err != nil {
			return "", "", err
		}
		last = services
		if err := allHealthy(services); err != nil {
			return "", "", err
		}
		if !m.cfg.Now().Before(deadline) {
			break
		}
		timer := time.NewTimer(healthPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", "", ctx.Err()
		case <-timer.C:
		}
	}
	if d.GetDomain() != "" {
		name := containerForService(last, d.GetService())
		if name == "" {
			return "", "", runtimeErr(fmt.Sprintf("service %q not found in compose ps", d.GetService()))
		}
		upstream := fmt.Sprintf("%s:%d", name, d.GetPort())
		if m.cfg.Ingress == nil {
			return "", "", ingressErr("ingress not available")
		}
		u, err := m.cfg.Ingress.Route(ctx, d.GetProject(), d.GetDomain(), upstream)
		if err != nil {
			return "", "", ingressErr(err.Error())
		}
		return u, "healthy at " + u, nil
	}
	return "", portsMessage(last), nil
}

func portsMessage(services []composePS) string {
	var parts []string
	for _, s := range services {
		for _, p := range s.Publishers {
			if p.PublishedPort > 0 {
				parts = append(parts, fmt.Sprintf("%s:%d->%d/%s", s.Service, p.PublishedPort, p.TargetPort, p.Protocol))
			}
		}
	}
	if len(parts) == 0 {
		return "healthy (no published ports)"
	}
	return "healthy; published ports: " + strings.Join(parts, ", ")
}

func containerForService(services []composePS, service string) string {
	for _, s := range services {
		if s.Service == service {
			return s.Name
		}
	}
	return ""
}

func allHealthy(services []composePS) error {
	if len(services) == 0 {
		return errors.New("no services reported by compose ps")
	}
	for _, s := range services {
		state := strings.ToLower(s.State)
		switch state {
		case "running":
			// ok
		case "restarting", "exited", "dead", "paused", "removing", "created":
			return fmt.Errorf("service %s state %s", s.Service, s.State)
		default:
			if state != "running" {
				return fmt.Errorf("service %s state %s", s.Service, s.State)
			}
		}
		health := strings.ToLower(s.Health)
		if health != "" && health != "healthy" {
			// "starting" during the window is not yet healthy.
			return fmt.Errorf("service %s health %s", s.Service, s.Health)
		}
	}
	return nil
}

func (m *Manager) composePS(ctx context.Context, project, composeFile string) ([]composePS, error) {
	var buf bytes.Buffer
	err := m.cfg.Runner.Run(ctx, Cmd{
		Name: "docker",
		Args: []string{"compose", "-p", project, "-f", composeFile, "ps", "--format", "json"},
		Stdout: func(line string) {
			buf.WriteString(line)
			buf.WriteByte('\n')
		},
	})
	if err != nil {
		return nil, fmt.Errorf("compose ps: %w", err)
	}
	return parseComposePS(buf.Bytes())
}

func parseComposePS(data []byte) ([]composePS, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	if data[0] == '[' {
		var list []composePS
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	var list []composePS
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var one composePS
		if err := dec.Decode(&one); err != nil {
			return nil, err
		}
		list = append(list, one)
	}
	return list, nil
}
