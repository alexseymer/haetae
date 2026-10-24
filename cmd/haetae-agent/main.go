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
	"syscall"

	"github.com/alexseymer/haetae/internal/agent"
	"github.com/alexseymer/haetae/internal/version"
)

const usage = `usage:
  haetae-agent enroll --server URL --token TOKEN --ca-pin PIN [--state-dir DIR]
  haetae-agent run [--state-dir DIR]
  haetae-agent --version
`

const defaultStateDir = "/var/lib/haetae-agent"

func main() {
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

func enroll(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	serverURL := fs.String("server", "", "gateway URL, e.g. https://haetae.example.com:8443")
	token := fs.String("token", "", "single-use enrollment token")
	caPin := fs.String("ca-pin", "", "CA fingerprint printed with the token (sha256:...)")
	stateDir := fs.String("state-dir", defaultStateDir, "where the agent keeps its identity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *serverURL == "" || *token == "" || *caPin == "" {
		return errors.New("--server, --token and --ca-pin are required")
	}

	id, err := agent.Enroll(ctx, *serverURL, *token, *caPin, *stateDir)
	if err != nil {
		return err
	}
	fmt.Printf("Enrolled as host %s. Start the agent with: haetae-agent run --state-dir %s\n", id.HostID, *stateDir)
	return nil
}

func run(ctx context.Context, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir, "where the agent keeps its identity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id, err := agent.LoadIdentity(*stateDir)
	if err != nil {
		return err
	}
	log = log.With("host_id", id.HostID)
	log.Info("agent starting", "server", id.ServerURL, "version", version.Version)
	return agent.Run(ctx, id, log)
}
