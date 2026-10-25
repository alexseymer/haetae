//go:build !linux

package scan

import (
	"fmt"
	"os/exec"
)

func (l *Launcher) setupCgroup() (string, bool, error) {
	return "", false, fmt.Errorf("cgroup v2 not available")
}

func (l *Launcher) teardownCgroup(string) {}

func (l *Launcher) startInCgroup(cmd *exec.Cmd, _ string) error {
	return cmd.Start()
}

func readOOMKill(string) bool     { return false }
func readMemoryPeak(string) int64 { return 0 }
