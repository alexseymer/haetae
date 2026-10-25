//go:build linux

package scan

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

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
		parent, err = agentCgroupDir(base)
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
	writes := []struct{ name, val string }{
		{"memory.max", strconv.FormatInt(mem, 10)},
		{"memory.swap.max", "0"},
		{"pids.max", strconv.Itoa(pidsMax(l))},
		{"cpu.weight", strconv.Itoa(defaultCPUWeight)},
	}
	for _, w := range writes {
		if err := os.WriteFile(filepath.Join(path, w.name), []byte(w.val), 0o644); err != nil { //nolint:gosec // G306: cgroup attribute files
			_ = os.Remove(path)
			return "", false, fmt.Errorf("write %s: %w", w.name, err)
		}
	}
	// When tests inject a fake hierarchy, seed kernel-owned files from the parent
	// so launcher tests can simulate oom_kill / memory.peak.
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
	parent := filepath.Dir(path)
	if raw, err := os.ReadFile(filepath.Join(path, "cgroup.procs")); err == nil { //nolint:gosec // G304: path we created
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			_ = os.WriteFile(filepath.Join(parent, "cgroup.procs"), []byte(line), 0o644) //nolint:gosec // G306: cgroup.procs
		}
	}
	_ = os.Remove(path)
}

func (l *Launcher) startInCgroup(cmd *exec.Cmd, cgroupPath string) error {
	// Start then move into the leaf via cgroup.procs. This works with real
	// cgroup v2 and with injectable fake hierarchies used by tests.
	// (CLONE_INTO_CGROUP / UseCgroupFD cannot target a non-cgroup test dir.)
	if err := cmd.Start(); err != nil {
		return err
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
		return nil
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	if err := os.WriteFile(filepath.Join(cgroupPath, "cgroup.procs"), []byte(pid), 0o644); err != nil { //nolint:gosec // G306: cgroup.procs
		killProcessGroup(cmd)
		_ = cmd.Wait()
		return fmt.Errorf("cgroup.procs: %w", err)
	}
	return nil
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
