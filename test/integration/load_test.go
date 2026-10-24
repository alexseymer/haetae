//go:build integration && load

package integration

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/agent"
)

const (
	loadContainers = 20
	rssBudget      = 50 << 20 // spec S10
)

// S10: the agent stays under 50 MB RSS on a host with 20 busy containers.
// The real binary runs with its production defaults (10 s sampling, 60 s
// snapshots, GOMEMLIMIT 40 MiB). Re-run in M3, M7 and M9 as the agent grows.
func TestAgentMemoryUnderLoad(t *testing.T) {
	requireDocker(t)
	duration := 2 * time.Minute
	if d, err := time.ParseDuration(os.Getenv("HAETAE_LOAD_DURATION")); err == nil {
		duration = d
	}

	e := newEnv(t)
	docker(t, "pull", "-q", "busybox:1.37")
	label := "haetae-load=" + uuid.NewString()[:8]
	for i := range loadContainers {
		// ~50 log lines/s each, with log files capped so disk stays small.
		docker(t, "run", "-d", "--rm", "--label", label,
			"--label", "com.docker.compose.project=load",
			"--label", fmt.Sprintf("com.docker.compose.service=svc%02d", i),
			"--log-opt", "max-size=2m", "--log-opt", "max-file=1",
			"busybox:1.37", "sh", "-c", `i=0; while true; do i=$((i+1)); echo "request $i served in 12ms status=200 path=/api/items"; usleep 20000; done`)
	}
	t.Cleanup(func() {
		ids := strings.Fields(docker(t, "ps", "-aq", "--filter", "label="+label))
		if len(ids) > 0 {
			_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
		}
	})

	dir := t.TempDir()
	id, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token("load-host"), e.pin(), dir)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "haetae-agent")
	if out, err := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", bin, "../../cmd/haetae-agent").CombinedOutput(); err != nil {
		t.Fatalf("build agent: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "run", "--state-dir", dir)
	cmd.Env = append(os.Environ(), "GOMEMLIMIT=") // use the binary's default
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	e.waitConnected(id.HostID, true, 30*time.Second)

	var maxRSS, sum, n uint64
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		rss := procStatusKB(t, cmd.Process.Pid, "VmRSS") << 10
		maxRSS = max(maxRSS, rss)
		sum += rss
		n++
		time.Sleep(time.Second)
	}
	hwm := procStatusKB(t, cmd.Process.Pid, "VmHWM") << 10

	cs, err := e.st.ListContainers(context.Background(), e.org.ID, uuid.MustParse(id.HostID))
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) < loadContainers {
		t.Fatalf("server sees %d containers, want >= %d (agent not doing its job)", len(cs), loadContainers)
	}
	// The stats path must have run for every container, or the RSS number
	// would be measured on an agent doing less than in production.
	withMetrics := 0
	for _, c := range cs {
		_, pts, err := e.st.ContainerSeries(context.Background(), e.org.ID, uuid.MustParse(id.HostID), c.ContainerID, "cpu_pct", time.Now().Add(-duration-time.Minute), time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if len(pts) > 0 {
			withMetrics++
		}
	}
	if withMetrics < loadContainers {
		t.Fatalf("only %d of %d containers have metrics", withMetrics, loadContainers)
	}

	// The log pipeline must have been running under this load, or the RSS
	// number would not cover it: 20 containers at ~50 lines/s each.
	ls, err := e.st.GetLogStats(context.Background(), e.org.ID, uuid.MustParse(id.HostID))
	if err != nil {
		t.Fatalf("no log stats from the agent: %v", err)
	}
	if ls.LinesIn < 20*50*30 {
		t.Fatalf("agent processed only %d log lines in %s, want at least %d", ls.LinesIn, duration, 20*50*30)
	}

	mib := func(b uint64) string { return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20)) }
	t.Logf("MEASURE S10 agent RSS over %s with %d containers (%d with metrics), %d log lines in, %d withheld: max %s, mean %s, peak (VmHWM) %s, budget %s",
		duration, loadContainers, withMetrics, ls.LinesIn, ls.LinesSkipped, mib(maxRSS), mib(sum/max(n, 1)), mib(hwm), mib(rssBudget))
	if hwm >= rssBudget {
		t.Fatalf("agent peak RSS %s exceeds the %s budget", mib(hwm), mib(rssBudget))
	}
}

func procStatusKB(t *testing.T, pid int, field string) uint64 {
	t.Helper()
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), field+":"); ok {
			kb, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return kb
		}
	}
	t.Fatalf("%s not in /proc/%d/status", field, pid)
	return 0
}
