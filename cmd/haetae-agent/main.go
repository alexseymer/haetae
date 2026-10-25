// Command haetae-agent is the Haetae host agent.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/alexseymer/haetae/internal/agent"
	"github.com/alexseymer/haetae/internal/agent/deploy"
	"github.com/alexseymer/haetae/internal/redact"
	"github.com/alexseymer/haetae/internal/version"
)

const usage = `usage:
  haetae-agent enroll --server URL --ca-pin PIN [--token TOKEN] [--state-dir DIR]
  haetae-agent run [--state-dir DIR]
  haetae-agent --version

enroll: --token wins over the HAETAE_ENROLL_TOKEN environment variable when both are set.
`

const defaultStateDir = "/var/lib/haetae-agent"

// defaultMemoryLimit is the Go heap target, below the 50 MB RSS budget
// (spec S10) so the GC works harder before the agent gets close to it.
// GOMEMLIMIT in the environment overrides it.
const defaultMemoryLimit = 40 << 20

func main() {
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(defaultMemoryLimit)
	}
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String("haetae-agent"))
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "enroll":
		err = enroll(ctx, args[1:])
	case "run":
		err = run(ctx, args[1:], log)
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

type enrollArgs struct {
	ServerURL string
	Token     string
	CAPin     string
	StateDir  string
}

// parseEnrollArgs parses enroll flags. The --token flag wins over HAETAE_ENROLL_TOKEN.
func parseEnrollArgs(args []string, getenv func(string) string) (enrollArgs, error) {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	serverURL := fs.String("server", "", "gateway URL, e.g. https://haetae.example.com:8443")
	token := fs.String("token", "", "single-use enrollment token (or HAETAE_ENROLL_TOKEN)")
	caPin := fs.String("ca-pin", "", "CA fingerprint printed with the token (sha256:...)")
	stateDir := fs.String("state-dir", defaultStateDir, "where the agent keeps its identity")
	if err := fs.Parse(args); err != nil {
		return enrollArgs{}, err
	}
	tok := *token
	if tok == "" {
		tok = getenv("HAETAE_ENROLL_TOKEN")
	}
	if *serverURL == "" || tok == "" || *caPin == "" {
		return enrollArgs{}, errors.New("--server, --token (or HAETAE_ENROLL_TOKEN) and --ca-pin are required")
	}
	return enrollArgs{
		ServerURL: *serverURL,
		Token:     tok,
		CAPin:     *caPin,
		StateDir:  *stateDir,
	}, nil
}

func enroll(ctx context.Context, args []string) error {
	ea, err := parseEnrollArgs(args, os.Getenv)
	if err != nil {
		return err
	}

	id, err := agent.Enroll(ctx, ea.ServerURL, ea.Token, ea.CAPin, ea.StateDir)
	if err != nil {
		return err
	}
	fmt.Printf("Enrolled as host %s. Start the agent with: haetae-agent run --state-dir %s\n", id.HostID, ea.StateDir)
	return nil
}

func run(ctx context.Context, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir, "where the agent keeps its identity")
	dockerSocket := fs.String("docker-socket", "/var/run/docker.sock", "Docker Engine API socket")
	procRoot := fs.String("proc-root", "/proc", "proc filesystem to read host metrics from")
	diskPath := fs.String("disk-path", "/", "filesystem whose usage is reported")
	interval := fs.Duration("interval", 10*time.Second, "metrics sampling interval")
	httpPort := fs.Int("ingress-http-port", 80, "host port published for HTTP ingress")
	httpsPort := fs.Int("ingress-https-port", 443, "host port published for HTTPS ingress")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id, err := agent.LoadIdentity(*stateDir)
	if err != nil {
		return err
	}
	log = log.With("host_id", id.HostID)
	log.Info("agent starting", "server", id.ServerURL, "version", version.Version)

	allowLocalGit := os.Getenv("HAETAE_AGENT_ALLOW_LOCAL_GIT") == "1"
	tlsInternal := os.Getenv("HAETAE_AGENT_CADDY_TLS") == "internal"

	// 30 batches at the default interval keep five minutes of metrics while
	// the control plane is unreachable.
	out := agent.NewOutbox(30)
	mon := agent.NewMonitor(agent.MonitorConfig{
		DockerSocket: *dockerSocket,
		ProcRoot:     *procRoot,
		DiskPath:     *diskPath,
		Interval:     *interval,
	}, out, log.With("component", "monitor"))
	logs := agent.NewLogManager(agent.LogConfig{}, mon.Docker(), out, log.With("component", "logs"))
	mon.OnRunning = func(running map[string]string) { logs.Sync(ctx, running) }

	runner := deploy.ExecRunner{}
	ing, err := newIngress(ingressConfig{
		StateDir:    *stateDir,
		HTTPPort:    *httpPort,
		HTTPSPort:   *httpsPort,
		Runner:      runner,
		TLSInternal: tlsInternal,
	})
	if err != nil {
		return err
	}
	redactor := redact.New()
	deploys := deploy.New(deploy.Config{
		StateDir:      *stateDir,
		AllowLocalGit: allowLocalGit,
		Runner:        runner,
		Ingress:       ing,
		Emit:          out,
		Redact: func(ctx context.Context, lines []string) ([]string, error) {
			res, err := redactor.RedactBatch(ctx, lines, 500*time.Millisecond)
			if err != nil {
				return nil, err
			}
			return res.Lines, nil
		},
	})
	defer deploys.Close()

	watcher := &deploy.Watcher{
		Runner:        runner,
		AllowLocalGit: allowLocalGit,
		Log:           log.With("component", "watch"),
	}
	go watcher.Run(ctx, out.PutBranchUpdate)

	go mon.Run(ctx)
	go logs.Run(ctx)

	return agent.Run(ctx, id, log, agent.RunOptions{
		Outbox:        out,
		OnConnected:   mon.Resync,
		DockerVersion: mon.DockerVersion,
		OnGreen:       logs.SetGreen,
		Handler:       logs.Handle,
		Deploys:       deploys,
		Watcher:       watcher,
	})
}
