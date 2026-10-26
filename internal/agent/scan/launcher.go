package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultMemBytes  = 256 << 20
	defaultTimeout   = 10 * time.Minute
	defaultPidsMax   = 64
	defaultCPUWeight = 10
	envMem           = "HAETAE_INVENTORY_MEM"
	envTimeout       = "HAETAE_INVENTORY_TIMEOUT"
	envHelper        = "HAETAE_INVENTORY_BIN"
	helperName       = "haetae-inventory"
)

// LaunchRequest is one helper invocation.
type LaunchRequest struct {
	// DockerImage uses --docker-image (local daemon). Mutually exclusive with Registry.
	DockerImage string
	// Registry uses --registry (ref@sha256:...).
	Registry string
	// DockerSocket passed as --docker-socket.
	DockerSocket string
	// ScanID is informational only (not passed to the helper).
	ScanID string
}

// Launcher runs haetae-inventory under a cgroup (or rlimit fallback).
type Launcher struct {
	// HelperPath overrides binary discovery. Empty = beside agent or env.
	HelperPath string
	// CgroupRoot overrides /sys/fs/cgroup for tests (fake hierarchy).
	CgroupRoot string
	// MemBytes overrides HAETAE_INVENTORY_MEM / default 256 MiB.
	MemBytes int64
	// Timeout overrides HAETAE_INVENTORY_TIMEOUT / default 10m.
	Timeout time.Duration
	// PidsMax defaults to 64.
	PidsMax int
	// LookPath is os/exec.LookPath; tests replace it.
	LookPath func(string) (string, error)
	// Now is time.Now; injectable.
	Now func() time.Time
}

// NewLauncher returns a Launcher with defaults.
func NewLauncher() *Launcher {
	return &Launcher{
		LookPath: exec.LookPath,
		Now:      time.Now,
	}
}

func (l *Launcher) memBytes() int64 {
	if l.MemBytes > 0 {
		return l.MemBytes
	}
	if v := os.Getenv(envMem); v != "" {
		if n, err := parseBytes(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMemBytes
}

func (l *Launcher) timeout() time.Duration {
	if l.Timeout > 0 {
		return l.Timeout
	}
	if v := os.Getenv(envTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultTimeout
}

func (l *Launcher) helper() (string, error) {
	if l.HelperPath != "" {
		return l.HelperPath, nil
	}
	if v := os.Getenv(envHelper); v != "" {
		return v, nil
	}
	exe, err := os.Executable()
	if err == nil {
		cand := filepath.Join(filepath.Dir(exe), helperName)
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	if l.LookPath != nil {
		if p, err := l.LookPath(helperName); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("haetae-inventory binary not found")
}

// Run starts the helper, classifies the outcome, and returns at most one parsed document.
func (l *Launcher) Run(ctx context.Context, req LaunchRequest) LaunchResult {
	args, err := buildArgs(req)
	if err != nil {
		return LaunchResult{Status: StatusFailed, Error: clipError(err.Error()), Limited: true}
	}
	bin, err := l.helper()
	if err != nil {
		return LaunchResult{Status: StatusFailed, Error: clipError(err.Error()), Limited: true}
	}

	timeout := l.timeout()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	env := withEnv(helperEnv(os.Environ()), "GOMEMLIMIT", strconv.FormatInt(l.memBytes(), 10))
	// newCmd is called again if starting inside the cgroup fails, since an
	// exec.Cmd cannot be started twice.
	newCmd := func() *exec.Cmd {
		stdout.Reset()
		stderr.Reset()
		cmd := exec.CommandContext(runCtx, bin, args...) //nolint:gosec // args are validated; no shell
		cmd.Stdout = &limitWriter{buf: &stdout, max: maxHelperOutput + 1}
		cmd.Stderr = &limitWriter{buf: &stderr, max: 64 << 10}
		// Only an allowlist of the agent's environment reaches the helper,
		// which parses untrusted image content; GOMEMLIMIT is replaced.
		cmd.Env = env
		// New process group so timeout can kill the whole tree.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd
	}

	cgroupPath, limited, cgroupErr := l.setupCgroup()
	if cgroupErr != nil {
		cgroupPath = ""
		limited = false
	}
	if cgroupPath != "" {
		defer l.teardownCgroup(cgroupPath)
	}

	started := l.now()
	cmd, err := l.startInCgroup(newCmd, cgroupPath)
	if err != nil {
		return LaunchResult{
			Status:  StatusFailed,
			Error:   clipError(fmt.Sprintf("start helper: %v", err)),
			Limited: limited,
		}
	}
	waitErr := cmd.Wait()
	finished := l.now()
	_ = started
	_ = finished

	oom := false
	var peak int64
	if cgroupPath != "" {
		oom = readOOMKill(cgroupPath)
		peak = readMemoryPeak(cgroupPath)
	}

	if runCtx.Err() == context.DeadlineExceeded || errors.Is(waitErr, context.DeadlineExceeded) {
		killProcessGroup(cmd)
		return LaunchResult{
			Status:  StatusTimeout,
			Error:   "inventory timed out",
			Limited: limited,
		}
	}
	if oom {
		return LaunchResult{
			Status:     StatusOOM,
			Error:      "inventory out of memory",
			Limited:    limited,
			PeakMemory: peak,
		}
	}

	out := stdout.Bytes()
	if len(out) > maxHelperOutput {
		return LaunchResult{
			Status:      StatusFailed,
			Error:       "inventory output too large",
			Limited:     limited,
			RawTooLarge: true,
			PeakMemory:  peak,
		}
	}

	exitCode := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return LaunchResult{
				Status:     StatusFailed,
				Error:      clipError(fmt.Sprintf("helper: %v; %s", waitErr, stderr.String())),
				Limited:    limited,
				PeakMemory: peak,
			}
		}
	}

	switch exitCode {
	case 0:
		doc, err := parseHelperJSON(out)
		if err != nil {
			return LaunchResult{
				Status:     StatusFailed,
				Error:      clipError("malformed inventory JSON: " + err.Error()),
				Limited:    limited,
				PeakMemory: peak,
			}
		}
		if !limited {
			doc.ToolVersion = appendLimitedFalse(doc.ToolVersion)
		}
		if peak == 0 {
			peak = doc.PeakRSSBytes
		}
		return LaunchResult{Status: StatusOK, Doc: doc, Limited: limited, PeakMemory: peak}
	case 2:
		return LaunchResult{
			Status:     StatusUnsupported,
			Error:      clipError(firstLine(stderr.String(), "unsupported image")),
			Limited:    limited,
			PeakMemory: peak,
		}
	case 3:
		return LaunchResult{
			Status:     StatusFailed,
			Error:      clipError(firstLine(stderr.String(), "too many packages")),
			Limited:    limited,
			PeakMemory: peak,
		}
	default:
		msg := firstLine(stderr.String(), fmt.Sprintf("inventory exit %d", exitCode))
		if cgroupErr != nil && !limited {
			msg = msg + " (cgroup: " + cgroupErr.Error() + ")"
		}
		return LaunchResult{
			Status:     StatusFailed,
			Error:      clipError(msg),
			Limited:    limited,
			PeakMemory: peak,
		}
	}
}

func (l *Launcher) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func buildArgs(req LaunchRequest) ([]string, error) {
	var args []string
	switch {
	case req.DockerImage != "" && req.Registry == "":
		if err := validateImageArg(req.DockerImage); err != nil {
			return nil, err
		}
		args = append(args, "--docker-image", req.DockerImage)
	case req.Registry != "" && req.DockerImage == "":
		if err := validateRegistryArg(req.Registry); err != nil {
			return nil, err
		}
		args = append(args, "--registry", req.Registry)
	default:
		return nil, fmt.Errorf("exactly one of DockerImage or Registry is required")
	}
	sock := req.DockerSocket
	if sock == "" {
		sock = "/var/run/docker.sock"
	}
	if strings.ContainsAny(sock, " \t\n\r;&|$`") {
		return nil, fmt.Errorf("invalid docker socket path")
	}
	args = append(args, "--docker-socket", sock, "--out", "-")
	return args, nil
}

func firstLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil && pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}

// withEnv returns env with key=value, removing any prior entries for key.
var helperEnvKeys = []string{
	"PATH", "HOME", "TMPDIR", "LANG", "TZ",
	"DOCKER_HOST", "DOCKER_API_VERSION", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
	"HAETAE_INVENTORY_TEST", // test-only switch checked by the helper itself
}

func helperEnv(env []string) []string {
	out := make([]string, 0, len(helperEnvKeys))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if slices.Contains(helperEnvKeys, k) {
			out = append(out, e)
		}
	}
	return out
}

func withEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		out = append(out, e)
	}
	return append(out, prefix+value)
}

func parseBytes(v string) (int64, error) {
	v = strings.TrimSpace(strings.ToLower(v))
	mult := int64(1)
	switch {
	case strings.HasSuffix(v, "gi") || strings.HasSuffix(v, "g"):
		mult = 1 << 30
		v = strings.TrimSuffix(strings.TrimSuffix(v, "gi"), "g")
	case strings.HasSuffix(v, "mi") || strings.HasSuffix(v, "m"):
		mult = 1 << 20
		v = strings.TrimSuffix(strings.TrimSuffix(v, "mi"), "m")
	case strings.HasSuffix(v, "ki") || strings.HasSuffix(v, "k"):
		mult = 1 << 10
		v = strings.TrimSuffix(strings.TrimSuffix(v, "ki"), "k")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}

// limitWriter stops after max bytes (keeps writing discarded).
type limitWriter struct {
	buf *bytes.Buffer
	max int
	n   int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	remain := w.max - w.n
	if remain <= 0 {
		w.n += len(p)
		return len(p), nil
	}
	if len(p) > remain {
		_, _ = w.buf.Write(p[:remain])
		w.n += len(p)
		return len(p), nil
	}
	n, err := w.buf.Write(p)
	w.n += n
	return n, err
}

var _ io.Writer = (*limitWriter)(nil)
