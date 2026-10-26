//go:build deploy

package secfacts_test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
	"github.com/alexseymer/haetae/internal/agent/secfacts"
	"github.com/alexseymer/haetae/internal/testutil/dockerclean"
)

func TestMisconfigsReportedThenResolved(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	prefix := "htest-" + suffix
	names := []string{
		prefix + "-priv",
		prefix + "-sock",
		prefix + "-root",
		prefix + "-dbport",
		prefix + "-hostnet",
		prefix + "-hostpid",
		prefix + "-sysadmin",
	}
	t.Cleanup(func() { _ = dockerclean.ForceRemove("htest-" + suffix) })

	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
	}

	// Deliberately bad set covering every misconfig kind (except cert_expiring,
	// which is covered by the TLS unit test).
	run("run", "-d", "--name", names[0], "--privileged", "busybox:1.37", "sleep", "3600")
	run("run", "-d", "--name", names[1], "-v", "/var/run/docker.sock:/var/run/docker.sock", "busybox:1.37", "sleep", "3600")
	run("run", "-d", "--name", names[2], "--user", "0", "busybox:1.37", "sleep", "3600")
	run("run", "-d", "--name", names[3], "-p", "0.0.0.0:15432:5432", "busybox:1.37", "sleep", "3600")
	run("run", "-d", "--name", names[4], "--network", "host", "busybox:1.37", "sleep", "3600")
	run("run", "-d", "--name", names[5], "--pid", "host", "busybox:1.37", "sleep", "3600")
	run("run", "-d", "--name", names[6], "--cap-add", "SYS_ADMIN", "busybox:1.37", "sleep", "3600")

	docker := dockerapi.New("/var/run/docker.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ms, err := secfacts.Collect(ctx, docker, secfacts.Options{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, m := range ms {
		for _, n := range names {
			if strings.HasPrefix(m.ContainerId, "") && (m.ContainerId != "" || true) {
				// Match by inspecting which containers we created.
			}
			_ = n
		}
		kinds[m.Kind] = true
	}
	// Filter to our containers by re-inspecting names → ids.
	idOf := map[string]string{}
	for _, n := range names {
		out, err := exec.Command("docker", "inspect", "-f", "{{.Id}}", n).Output()
		if err != nil {
			t.Fatal(err)
		}
		idOf[n] = strings.TrimSpace(string(out))
	}
	ours := map[string]bool{}
	for _, m := range ms {
		for _, id := range idOf {
			if m.ContainerId == id || strings.HasPrefix(id, m.ContainerId) || strings.HasPrefix(m.ContainerId, id[:12]) {
				ours[m.Kind] = true
			}
		}
	}
	want := []string{
		secfacts.KindPrivileged,
		secfacts.KindDockerSocket,
		secfacts.KindRootUser,
		secfacts.KindPublishedPort,
		secfacts.KindHostNetwork,
		secfacts.KindHostPID,
		secfacts.KindDangerousCapability,
	}
	for _, k := range want {
		if !ours[k] {
			t.Errorf("missing kind %s; all kinds=%v ours=%v", k, kinds, ours)
		}
	}

	// Fix/remove the bad containers and assert findings resolve.
	for _, n := range names {
		_ = exec.Command("docker", "rm", "-f", n).Run()
	}
	ms2, err := secfacts.Collect(ctx, docker, secfacts.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms2 {
		for _, id := range idOf {
			if m.ContainerId == id || strings.HasPrefix(id, m.ContainerId) {
				t.Fatalf("finding still present after removal: %#v", m)
			}
		}
	}
}
