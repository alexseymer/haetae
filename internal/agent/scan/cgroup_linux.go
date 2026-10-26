//go:build linux

package scan

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func (l *Launcher) cgroupBase() string {
	if l.CgroupRoot != "" {
		return l.CgroupRoot
	}
	return "/sys/fs/cgroup"
}

// setupCgroup creates a leaf under the agent's cgroup. limited=false when
// falling back to rlimits only.
func (l *Launcher) setupCgroup() (path string, limited bool, err error) {
	base := l.cgroupBase()
	var parent string
	if l.CgroupRoot != "" {
		// Tests inject a writable fake hierarchy; use it as the parent leaf.
		parent = base
		if err := os.MkdirAll(parent, 0o755); err != nil { //nolint:gosec // G301: cgroup dirs are 0755
			return "", false, err
		}
	} else {
		var err error
		parent, err = delegatedParent(base)
		if err != nil {
			return "", false, err
		}
	}
	id := strconv.FormatInt(l.now().UnixNano(), 10)
	path = filepath.Join(parent, "haetae-inventory-"+id)
	if err := os.Mkdir(path, 0o755); err != nil { //nolint:gosec // G301: kernel cgroup leaf mode
		return "", false, err
	}
	mem := l.memBytes()
	writes := []struct {
		name, val string
		required  bool
	}{
		{"memory.max", strconv.FormatInt(mem, 10), true},
		{"pids.max", strconv.Itoa(pidsMax(l)), true},
		{"memory.swap.max", "0", false}, // absent without swap accounting
		{"cpu.weight", strconv.Itoa(defaultCPUWeight), false},
	}
	for _, w := range writes {
		err := os.WriteFile(filepath.Join(path, w.name), []byte(w.val), 0o644) //nolint:gosec // G306: cgroup attribute files
		if err != nil && w.required {
			_ = os.Remove(path)
			return "", false, fmt.Errorf("write %s: %w", w.name, err)
		}
	}
	if l.CgroupRoot != "" {
		for _, name := range []string{"memory.events", "memory.peak", "cgroup.procs"} {
			src := filepath.Join(parent, name)
			if raw, err := os.ReadFile(src); err == nil { //nolint:gosec // G304: test fake cgroup
				_ = os.WriteFile(filepath.Join(path, name), raw, 0o644) //nolint:gosec // G306: test fake cgroup
			}
		}
	}
	return path, true, nil
}

func pidsMax(l *Launcher) int {
	if l.PidsMax > 0 {
		return l.PidsMax
	}
	return defaultPidsMax
}

func (l *Launcher) teardownCgroup(path string) {
	if path == "" {
		return
	}
	// Kill whatever is left in the leaf. Never migrate it to the parent: that
	// would let a stray helper process run on outside the memory limit.
	if err := os.WriteFile(filepath.Join(path, "cgroup.kill"), []byte("1"), 0o644); err != nil { //nolint:gosec // G306: cgroup.kill
		for _, pid := range livePids(path) { // kernels before 5.14 have no cgroup.kill
			_ = unix.Kill(pid, unix.SIGKILL)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(livePids(path)) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	_ = os.Remove(path) // fails (and leaks an empty leaf) only if a process survived SIGKILL
}

func livePids(cgroupPath string) []int {
	raw, err := os.ReadFile(filepath.Join(cgroupPath, "cgroup.procs")) //nolint:gosec // G304: path we created
	if err != nil {
		return nil
	}
	var out []int
	for _, line := range strings.Split(string(raw), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid <= 0 {
			continue
		}
		if unix.Kill(pid, 0) == nil {
			out = append(out, pid)
		}
	}
	return out
}

func (l *Launcher) startInCgroup(newCmd func() *exec.Cmd, cgroupPath string) (*exec.Cmd, error) {
	cmd := newCmd()
	if cgroupPath != "" {
		// Start the helper directly inside the leaf (clone3 CLONE_INTO_CGROUP,
		// Linux 5.7+) so it never runs outside the limits.
		if fd, err := unix.Open(cgroupPath, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0); err == nil {
			cmd.SysProcAttr.UseCgroupFD = true
			cmd.SysProcAttr.CgroupFD = fd
			err = cmd.Start()
			_ = unix.Close(fd)
			if err == nil {
				return cmd, nil
			}
			// Older kernel or not a cgroup directory (test fakes): fall back
			// to moving the process right after start.
			cmd = newCmd()
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if cgroupPath == "" {
		// RLIMIT_AS caps virtual address space (including mmap), not RSS.
		// Syft+cgo needs far more VA than peak RSS (1 GiB AS fails on
		// alpine; 2 GiB works), so allow 8× GOMEMLIMIT while the heap
		// soft-cap stays at memBytes via GOMEMLIMIT.
		mem := uint64(l.memBytes()) //nolint:gosec // G115: configured limit
		as := mem * 8
		if as < mem {
			as = mem
		}
		lim := unix.Rlimit{Cur: as, Max: as}
		if err := unix.Prlimit(cmd.Process.Pid, unix.RLIMIT_AS, &lim, nil); err != nil {
			// Best-effort: GOMEMLIMIT is still set on the child.
			_ = err
		}
		return cmd, nil
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	if err := os.WriteFile(filepath.Join(cgroupPath, "cgroup.procs"), []byte(pid), 0o644); err != nil { //nolint:gosec // G306: cgroup.procs
		killProcessGroup(cmd)
		_ = cmd.Wait()
		return nil, fmt.Errorf("cgroup.procs: %w", err)
	}
	return cmd, nil
}

func agentCgroupDir(base string) (string, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	rel := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "0::") {
			rel = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if rel == "" {
		return "", fmt.Errorf("no cgroup v2 path in /proc/self/cgroup")
	}
	dir := filepath.Join(base, filepath.Clean("/"+rel))
	if _, err := os.Stat(filepath.Join(dir, "cgroup.procs")); err != nil { //nolint:gosec // G703: cleaned path under /sys/fs/cgroup
		return "", err
	}
	probe := filepath.Join(dir, ".haetae-probe")
	if err := os.Mkdir(probe, 0o755); err != nil { //nolint:gosec // G301: cgroup dir mode
		return "", err
	}
	_ = os.Remove(probe) //nolint:gosec // G703: probe under cleaned cgroup path
	return dir, nil
}

func readOOMKill(cgroupPath string) bool {
	raw, err := os.ReadFile(filepath.Join(cgroupPath, "memory.events")) //nolint:gosec
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "oom_kill" {
			n, _ := strconv.Atoi(fields[1])
			return n > 0
		}
	}
	return false
}

func readMemoryPeak(cgroupPath string) int64 {
	raw, err := os.ReadFile(filepath.Join(cgroupPath, "memory.peak")) //nolint:gosec
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// agentLeaf holds the agent's own process once it has delegated its cgroup.
const agentLeaf = "agent"

// delegatedParent returns the agent's service cgroup with the memory and
// pids controllers enabled for children. cgroup v2 only lets a cgroup
// without processes hand controllers down, so the agent first moves itself
// into the "agent" leaf; inventory leaves become its siblings. The systemd
// unit sets Delegate=yes so this subtree belongs to the agent user.
func delegatedParent(base string) (string, error) {
	dir, err := agentCgroupDir(base)
	if err != nil {
		return "", err
	}
	if filepath.Clean(dir) == filepath.Clean(base) {
		return dir, nil // root cgroup: no internal-process rule
	}
	if filepath.Base(dir) == agentLeaf {
		dir = filepath.Dir(dir)
	} else {
		leaf := filepath.Join(dir, agentLeaf)
		if err := os.Mkdir(leaf, 0o755); err != nil && !os.IsExist(err) { //nolint:gosec // G301: cgroup dir mode
			return "", err
		}
		if err := os.WriteFile(filepath.Join(leaf, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil { //nolint:gosec // G306: cgroup.procs
			return "", fmt.Errorf("move agent into %s: %w", agentLeaf, err)
		}
	}
	if err := enableControllers(dir); err != nil {
		return "", err
	}
	// The unit's MemoryMax now covers agent plus helper; keep the agent's own
	// budget on its leaf (best effort: the unit limit still applies).
	leaf := filepath.Join(dir, agentLeaf)
	_ = os.WriteFile(filepath.Join(leaf, "memory.high"), []byte(strconv.Itoa(agentMemHigh)), 0o644) //nolint:gosec // G306: cgroup attribute
	_ = os.WriteFile(filepath.Join(leaf, "memory.max"), []byte(strconv.Itoa(agentMemMax)), 0o644)   //nolint:gosec // G306: cgroup attribute
	return dir, nil
}

const (
	agentMemHigh = 60 << 20
	agentMemMax  = 100 << 20
)

func enableControllers(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "cgroup.subtree_control")) //nolint:gosec // G304: own cgroup
	if err != nil {
		return err
	}
	have := strings.Fields(string(raw))
	var add []string
	for _, c := range []string{"memory", "pids"} {
		if !slices.Contains(have, c) {
			add = append(add, "+"+c)
		}
	}
	if len(add) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte(strings.Join(add, " ")), 0o644); err != nil { //nolint:gosec // G306: cgroup control file
			return fmt.Errorf("enable %s: %w", strings.Join(add, " "), err)
		}
	}
	if !slices.Contains(have, "cpu") {
		_ = os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+cpu"), 0o644) //nolint:gosec // G306: optional controller
	}
	return nil
}
