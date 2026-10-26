//go:build bench

package bench

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/agent"
	"github.com/alexseymer/haetae/internal/gateway"
	"github.com/alexseymer/haetae/internal/pki"
	"github.com/alexseymer/haetae/internal/server"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

const (
	rssBudget     = 50 << 20
	rssContainers = 4
	defaultRSSDur = 3 * time.Minute
)

type benchEnv struct {
	t    *testing.T
	st   *store.Store
	ca   *pki.CA
	org  store.Org
	addr string
	log  *slog.Logger
	gw   *gateway.Gateway
	gs   io.Closer
}

// TestAgentRSSWithScanSecurity samples VmRSS/VmHWM for 3 minutes with the
// real haetae-agent binary (scan scheduler + secfacts/conntrack started) and
// a few containers. Pattern follows test/integration.TestAgentMemoryUnderLoad (M2).
func TestAgentRSSWithScanSecurity(t *testing.T) {
	requireDocker(t)
	duration := defaultRSSDur
	if d, err := time.ParseDuration(os.Getenv("HAETAE_BENCH_RSS_DURATION")); err == nil {
		duration = d
	}

	e := newBenchEnv(t)
	docker(t, "pull", "-q", "busybox:1.37")
	label := "haetae-m7-rss=" + uuid.NewString()[:8]
	for i := range rssContainers {
		docker(t, "run", "-d", "--rm", "--name", fmt.Sprintf("haetae-m7-rss-%d-%s", i, label[len(label)-8:]),
			"--label", label,
			"--label", "com.docker.compose.project=m7rss",
			"--label", fmt.Sprintf("com.docker.compose.service=svc%02d", i),
			"busybox:1.37", "sh", "-c", "while true; do echo ok; sleep 5; done")
	}
	t.Cleanup(func() {
		ids := strings.Fields(docker(t, "ps", "-aq", "--filter", "label="+label))
		if len(ids) > 0 {
			_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
		}
	})

	dir := t.TempDir()
	binDir := t.TempDir()
	agentBin := filepath.Join(binDir, "haetae-agent")
	invBin := filepath.Join(binDir, "haetae-inventory")
	if out, err := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", agentBin, "../../cmd/haetae-agent").CombinedOutput(); err != nil {
		t.Fatalf("build agent: %v\n%s", err, out)
	}
	if out, err := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", invBin, "../../cmd/haetae-inventory").CombinedOutput(); err != nil {
		t.Fatalf("build inventory: %v\n%s", err, out)
	}

	id, err := agent.Enroll(context.Background(), "https://"+e.addr, e.token("m7-rss-host"), e.pin(), dir)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(agentBin, "run", "--state-dir", dir)
	cmd.Env = append(os.Environ(), "GOMEMLIMIT=")
	cmd.Dir = binDir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	e.waitConnected(id.HostID, true, 45*time.Second)

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
	if len(cs) < 1 {
		t.Fatalf("server sees %d containers, want >= 1", len(cs))
	}

	mib := func(b uint64) string { return fmt.Sprintf("%.2f MiB", float64(b)/(1<<20)) }
	mean := uint64(0)
	if n > 0 {
		mean = sum / n
	}
	line := fmt.Sprintf("MEASURE M7 agent RSS over %s with %d containers (scan+security active): max %s, mean %s, peak (VmHWM) %s, budget %s, samples %d, host_containers_seen %d",
		duration, rssContainers, mib(maxRSS), mib(mean), mib(hwm), mib(rssBudget), n, len(cs))
	t.Log(line)

	outPath := os.Getenv("HAETAE_BENCH_RSS_OUT")
	if outPath != "" {
		payload := map[string]any{
			"duration":           duration.String(),
			"containers":         rssContainers,
			"maxRssBytes":        maxRSS,
			"meanRssBytes":       mean,
			"peakVmHWMBytes":     hwm,
			"budgetBytes":        rssBudget,
			"samples":            n,
			"hostContainersSeen": len(cs),
			"note":               "real conntrack RSS not measurable here (no usable nf_conntrack for the agent); see conntrack fake-source heap bench",
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outPath, b, 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(outPath+".txt", []byte(line+"\n"), 0o644)
	}
	if hwm >= rssBudget {
		t.Fatalf("agent peak RSS %s exceeds the %s budget", mib(hwm), mib(rssBudget))
	}
}

func newBenchEnv(t *testing.T) *benchEnv {
	t.Helper()
	st := storetest.New(t)
	if err := st.EnsurePartitions(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	ca, err := pki.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	org, err := st.DefaultOrg(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e := &benchEnv{
		t:    t,
		st:   st,
		ca:   ca,
		org:  org,
		addr: freeAddr(t),
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	e.gw = gateway.New(e.st, e.ca, gateway.Config{Heartbeat: time.Second}, e.log)
	gs, err := server.ListenGateway(e.addr, e.gw, e.ca, []string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	e.gs = gs
	t.Cleanup(func() {
		if e.gs != nil {
			_ = e.gs.Close()
		}
	})
	return e
}

func (e *benchEnv) token(name string) string {
	e.t.Helper()
	tok, _, err := e.st.CreateEnrollmentToken(context.Background(), e.org.ID, name, time.Hour)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func (e *benchEnv) pin() string { return pki.Fingerprint(e.ca.DER) }

func (e *benchEnv) waitConnected(id string, want bool, timeout time.Duration) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		h, err := e.st.GetHost(context.Background(), e.org.ID, uuid.MustParse(id))
		if err == nil && h.Connected == want {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.t.Fatalf("host %s connected!=%v after %s", id, want, timeout)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func requireDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker not available")
	}
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
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
