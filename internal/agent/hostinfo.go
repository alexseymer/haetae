package agent

import (
	"bufio"
	"os"
	"runtime"
	"strings"
	"syscall"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
	"github.com/alexseymer/haetae/internal/version"
)

// collectHostInfo describes the machine the agent runs on. Fields it cannot
// determine are left empty.
func collectHostInfo() *agentv1.HostInfo {
	hostname, _ := os.Hostname()
	return &agentv1.HostInfo{
		Hostname:     hostname,
		Os:           osPrettyName("/etc/os-release"),
		Kernel:       kernelRelease(),
		Arch:         runtime.GOARCH,
		AgentVersion: version.Version,
	}
}

func osPrettyName(path string) string {
	f, err := os.Open(path) //nolint:gosec // G304: path is a fixed system file, not user input
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func kernelRelease() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return ""
	}
	b := make([]byte, 0, len(u.Release))
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		b = append(b, byte(c)) //nolint:gosec // G115: utsname bytes are int8 or uint8 by arch; ASCII either way
	}
	return string(b)
}
