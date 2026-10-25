// Package ingress manages the agent's HTTPS edge: one Caddy container and a
// Caddyfile derived from a local routes file.
package ingress

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alexseymer/haetae/internal/deployspec"
)

// defaultImage is the pinned Caddy image tag.
// Resolved digest (multi-arch index): sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d
// (docker pull caddy:2.10.2-alpine → caddy@sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d).
const defaultImage = "caddy:2.10.2-alpine"

const (
	defaultContainerName = "haetae-caddy"
	defaultVolumePrefix  = "haetae-caddy"
	reloadTimeout        = 20 * time.Second
	maxLineBytes         = 64 << 10
)

// upstreamRE matches container-name:port (validated further for port range).
var upstreamRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}:[0-9]{1,5}$`)

// Runner runs an external command with no shell and streams each output line
// (stdout and stderr) to onLine when non-nil.
//
// This interface is intentionally smaller than deploy.Runner (Cmd with
// Dir/Env/Stdout/Stderr) so this package does not import internal/agent/deploy.
// W1's wiring adapts deploy.Runner to this shape when constructing Ingress.
type Runner interface {
	Run(ctx context.Context, name string, args []string, onLine func(string)) error
}

// Config configures Ingress. Zero ports default to 80/443; empty Image,
// ContainerName and VolumePrefix get their defaults. A nil Runner uses a
// local os/exec implementation; a nil Now uses time.Now.
type Config struct {
	StateDir      string
	HTTPPort      int  // host publish; default 80
	HTTPSPort     int  // host publish; default 443
	TLSInternal   bool // Caddy internal CA (tests); omit for Let's Encrypt
	Image         string
	ContainerName string
	VolumePrefix  string // volumes <prefix>-data and <prefix>-config; default "haetae-caddy"
	Runner        Runner
	Now           func() time.Time
	Log           *slog.Logger
}

// route is one project's ingress target, persisted in routes.json.
type route struct {
	Domain   string `json:"domain"`
	Upstream string `json:"upstream"`
}

// Ingress keeps routes on disk and drives one Caddy container.
type Ingress struct {
	stateDir      string
	ingressDir    string
	routesPath    string
	caddyfilePath string
	httpPort      int
	httpsPort     int
	tlsInternal   bool
	image         string
	containerName string
	volumePrefix  string
	runner        Runner
	now           func() time.Time
	log           *slog.Logger

	mu     sync.Mutex
	routes map[string]route
}

// New validates cfg, creates <StateDir>/ingress/ (0700), and loads routes.json
// if present. No docker commands run until Route or Restore.
//
// Restore re-applies existing routes and re-ensures the container; W1's wiring
// calls it at agent start. Nothing in this package calls Restore.
func New(cfg Config) (*Ingress, error) {
	if cfg.StateDir == "" {
		return nil, errors.New("ingress: StateDir is required")
	}
	httpPort := cfg.HTTPPort
	if httpPort == 0 {
		httpPort = 80
	}
	httpsPort := cfg.HTTPSPort
	if httpsPort == 0 {
		httpsPort = 443
	}
	if err := checkPort(httpPort); err != nil {
		return nil, fmt.Errorf("ingress: HTTPPort: %w", err)
	}
	if err := checkPort(httpsPort); err != nil {
		return nil, fmt.Errorf("ingress: HTTPSPort: %w", err)
	}

	image := cfg.Image
	if image == "" {
		image = defaultImage
	}
	name := cfg.ContainerName
	if name == "" {
		name = defaultContainerName
	}
	volPrefix := cfg.VolumePrefix
	if volPrefix == "" {
		volPrefix = defaultVolumePrefix
	}
	runner := cfg.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}

	ingressDir := filepath.Join(cfg.StateDir, "ingress")
	if err := os.MkdirAll(ingressDir, 0o700); err != nil {
		return nil, fmt.Errorf("ingress: mkdir: %w", err)
	}

	ing := &Ingress{
		stateDir:      cfg.StateDir,
		ingressDir:    ingressDir,
		routesPath:    filepath.Join(ingressDir, "routes.json"),
		caddyfilePath: filepath.Join(ingressDir, "Caddyfile"),
		httpPort:      httpPort,
		httpsPort:     httpsPort,
		tlsInternal:   cfg.TLSInternal,
		image:         image,
		containerName: name,
		volumePrefix:  volPrefix,
		runner:        runner,
		now:           now,
		log:           log,
		routes:        make(map[string]route),
	}
	if err := ing.loadRoutes(); err != nil {
		return nil, err
	}
	return ing, nil
}

// Route adds or updates a project's domain → upstream mapping, ensures the
// Caddy container, connects it to the upstream's networks, reloads, and
// returns the public HTTPS URL.
func (i *Ingress) Route(ctx context.Context, project, domain, upstream string) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := validateRoute(project, domain, upstream); err != nil {
		return "", err
	}
	for other, r := range i.routes {
		if other != project && r.Domain == domain {
			return "", fmt.Errorf("ingress: domain %q is already claimed by project %q", domain, other)
		}
	}

	i.routes[project] = route{Domain: domain, Upstream: upstream}
	if err := i.saveRoutes(); err != nil {
		delete(i.routes, project)
		return "", err
	}
	if err := i.writeCaddyfile(); err != nil {
		return "", err
	}
	if err := i.ensureContainer(ctx); err != nil {
		return "", err
	}
	if err := i.connectNetworks(ctx, upstream); err != nil {
		return "", err
	}
	if err := i.reload(ctx); err != nil {
		return "", err
	}
	return i.publicURL(domain), nil
}

// Unroute removes a project's route and reloads Caddy. It does not stop the
// container or remove other routes.
func (i *Ingress) Unroute(ctx context.Context, project string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := deployspec.ProjectName(project); err != nil {
		return fmt.Errorf("ingress: project: %w", err)
	}
	if _, ok := i.routes[project]; !ok {
		return nil
	}
	delete(i.routes, project)
	if err := i.saveRoutes(); err != nil {
		return err
	}
	if err := i.writeCaddyfile(); err != nil {
		return err
	}
	if err := i.ensureContainer(ctx); err != nil {
		return err
	}
	return i.reload(ctx)
}

// Restore re-writes the Caddyfile from routes.json, ensures the container,
// connects networks for every route, and reloads. W1's wiring calls this at
// agent start; this package never calls it.
func (i *Ingress) Restore(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if len(i.routes) == 0 {
		return nil
	}
	if err := i.writeCaddyfile(); err != nil {
		return err
	}
	if err := i.ensureContainer(ctx); err != nil {
		return err
	}
	for _, r := range i.routes {
		if err := i.connectNetworks(ctx, r.Upstream); err != nil {
			return err
		}
	}
	return i.reload(ctx)
}

// Close releases resources. The Caddy container is left running.
func (i *Ingress) Close() {}

func checkPort(p int) error {
	if p < 1 || p > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func validateRoute(project, domain, upstream string) error {
	if err := deployspec.ProjectName(project); err != nil {
		return fmt.Errorf("ingress: project: %w", err)
	}
	if domain == "" {
		return errors.New("ingress: domain is required")
	}
	if err := deployspec.Domain(domain); err != nil {
		return fmt.Errorf("ingress: domain: %w", err)
	}
	if err := validateUpstream(upstream); err != nil {
		return fmt.Errorf("ingress: upstream: %w", err)
	}
	return nil
}

func validateUpstream(s string) error {
	if !upstreamRE.MatchString(s) {
		return errors.New("must be container-name:port")
	}
	_, portStr, ok := strings.Cut(s, ":")
	if !ok {
		return errors.New("must be container-name:port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func (i *Ingress) publicURL(domain string) string {
	if i.httpsPort == 443 {
		return "https://" + domain
	}
	return fmt.Sprintf("https://%s:%d", domain, i.httpsPort)
}

func (i *Ingress) loadRoutes() error {
	data, err := os.ReadFile(i.routesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("ingress: read routes: %w", err)
	}
	var routes map[string]route
	if err := json.Unmarshal(data, &routes); err != nil {
		return fmt.Errorf("ingress: parse routes: %w", err)
	}
	if routes == nil {
		routes = make(map[string]route)
	}
	valid := make(map[string]route, len(routes))
	for project, r := range routes {
		if err := validateRoute(project, r.Domain, r.Upstream); err != nil {
			i.log.Error("ingress: dropping invalid restored route",
				"project", project, "domain", r.Domain, "upstream", r.Upstream, "err", err)
			continue
		}
		valid[project] = r
	}
	i.routes = valid
	return nil
}

func (i *Ingress) saveRoutes() error {
	data, err := json.MarshalIndent(i.routes, "", "  ")
	if err != nil {
		return fmt.Errorf("ingress: encode routes: %w", err)
	}
	data = append(data, '\n')
	tmp := i.routesPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("ingress: write routes: %w", err)
	}
	if err := os.Rename(tmp, i.routesPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("ingress: rename routes: %w", err)
	}
	return nil
}

// writeCaddyfile writes the Caddyfile in place (truncate+write+fsync). A single
// file bind mount breaks if the path is replaced by rename.
func (i *Ingress) writeCaddyfile() error {
	content := writeCaddyfileContent(i.routes, i.tlsInternal)
	f, err := os.OpenFile(i.caddyfilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) //nolint:gosec // G302: read by the Caddy container, contains no secrets
	if err != nil {
		return fmt.Errorf("ingress: open Caddyfile: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("ingress: write Caddyfile: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("ingress: sync Caddyfile: %w", err)
	}
	return nil
}

func (i *Ingress) ensureContainer(ctx context.Context) error {
	running, err := i.inspectRunning(ctx)
	if err != nil {
		return i.runContainer(ctx)
	}
	if !running {
		return i.startContainer(ctx)
	}
	return nil
}

func (i *Ingress) inspectRunning(ctx context.Context) (bool, error) {
	var out strings.Builder
	err := i.runner.Run(ctx, "docker", []string{
		"inspect", "-f", "{{.State.Running}}", i.containerName,
	}, func(line string) {
		out.WriteString(line)
	})
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(out.String()) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("ingress: unexpected inspect output %q", out.String())
	}
}

func (i *Ingress) runContainer(ctx context.Context) error {
	args := []string{
		"run", "-d",
		"--name", i.containerName,
		"--restart", "unless-stopped",
		"-p", fmt.Sprintf("%d:80", i.httpPort),
		"-p", fmt.Sprintf("%d:443", i.httpsPort),
		"-v", i.caddyfilePath + ":/etc/caddy/Caddyfile:ro",
		"-v", i.volumePrefix + "-data:/data",
		"-v", i.volumePrefix + "-config:/config",
		"--cap-drop", "ALL",
		"--cap-add", "NET_BIND_SERVICE",
		"--security-opt", "no-new-privileges",
		i.image,
	}
	return i.runner.Run(ctx, "docker", args, nil)
}

func (i *Ingress) startContainer(ctx context.Context) error {
	return i.runner.Run(ctx, "docker", []string{"start", i.containerName}, nil)
}

func (i *Ingress) connectNetworks(ctx context.Context, upstream string) error {
	container, _, ok := strings.Cut(upstream, ":")
	if !ok || container == "" {
		return errors.New("ingress: upstream missing container name")
	}
	var out strings.Builder
	err := i.runner.Run(ctx, "docker", []string{
		"inspect", "-f", "{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}", container,
	}, func(line string) {
		out.WriteString(line)
	})
	if err != nil {
		return fmt.Errorf("ingress: inspect upstream networks: %w", err)
	}
	for _, net := range strings.Fields(out.String()) {
		cerr := i.runner.Run(ctx, "docker", []string{"network", "connect", net, i.containerName}, nil)
		if cerr != nil && !isAlreadyExists(cerr) {
			return fmt.Errorf("ingress: network connect %s: %w", net, cerr)
		}
	}
	return nil
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "already connected")
}

func (i *Ingress) reload(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()
	args := []string{
		"exec", i.containerName,
		"caddy", "reload",
		"--config", "/etc/caddy/Caddyfile",
		"--address", "unix//run/caddy-admin.sock",
		"--force",
	}
	var last error
	for {
		last = i.runner.Run(rctx, "docker", args, nil)
		if last == nil {
			return nil
		}
		if rctx.Err() != nil {
			if last != nil && !errors.Is(last, context.DeadlineExceeded) && !errors.Is(last, context.Canceled) {
				return fmt.Errorf("ingress: caddy reload: %w (last: %w)", rctx.Err(), last)
			}
			return fmt.Errorf("ingress: caddy reload: %w", rctx.Err())
		}
		// Fresh containers may not accept exec until Caddy is up; retry until timeout.
		select {
		case <-rctx.Done():
			return fmt.Errorf("ingress: caddy reload: %w (last: %w)", rctx.Err(), last)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ExecRunner runs commands with os/exec (no shell). Used when Config.Runner is nil.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args []string, onLine func(string)) error {
	if name == "" {
		return errors.New("ingress: empty command name")
	}
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: Runner executes only binaries chosen by the code with validated arguments
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = nil

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()

	var (
		wg        sync.WaitGroup
		stderrBuf strings.Builder
	)
	scan := func(r io.Reader, capture *strings.Builder) {
		defer wg.Done()
		br := bufio.NewReaderSize(r, maxLineBytes)
		for {
			line, rerr := readLine(br)
			if len(line) > 0 {
				if onLine != nil {
					onLine(line)
				}
				if capture != nil {
					if capture.Len() > 0 {
						capture.WriteByte('\n')
					}
					capture.WriteString(line)
				}
			}
			if rerr != nil {
				return
			}
		}
	}
	wg.Add(2)
	go scan(stdout, nil)
	go scan(stderr, &stderrBuf)
	wg.Wait()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		msg := strings.TrimSpace(stderrBuf.String())
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func readLine(br *bufio.Reader) (string, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(chunk) > 0 {
			if len(buf)+len(chunk) > maxLineBytes {
				need := maxLineBytes - len(buf)
				if need > 0 {
					buf = append(buf, chunk[:need]...)
				}
				for isPrefix && err == nil {
					_, isPrefix, err = br.ReadLine()
				}
				return string(buf), err
			}
			buf = append(buf, chunk...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(buf) > 0 {
				return string(buf), nil
			}
			return string(buf), err
		}
		if !isPrefix {
			return string(buf), nil
		}
	}
}
