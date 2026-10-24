// Command haetae-server is the Haetae control plane.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alexseymer/haetae/internal/gateway"
	"github.com/alexseymer/haetae/internal/pki"
	"github.com/alexseymer/haetae/internal/server"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/version"
)

const usage = `usage:
  haetae-server serve                     run the control plane
  haetae-server token create --name NAME  create a single-use enrollment token
  haetae-server --version

Configuration (environment):
  HAETAE_DATABASE_URL    PostgreSQL URL (required)
  HAETAE_DATA_DIR        state directory, holds the agent CA (default /var/lib/haetae)
  HAETAE_GATEWAY_ADDR    agent gateway listen address (default :8443)
  HAETAE_GATEWAY_URL     URL agents dial (default https://<hostname>:8443)
  HAETAE_GATEWAY_NAMES   extra comma-separated names for the gateway certificate
`

type config struct {
	databaseURL  string
	dataDir      string
	gatewayAddr  string
	gatewayURL   string
	gatewayNames []string
}

func loadConfig() (config, error) {
	hostname, _ := os.Hostname()
	c := config{
		databaseURL: os.Getenv("HAETAE_DATABASE_URL"),
		dataDir:     envOr("HAETAE_DATA_DIR", "/var/lib/haetae"),
		gatewayAddr: envOr("HAETAE_GATEWAY_ADDR", ":8443"),
		gatewayURL:  envOr("HAETAE_GATEWAY_URL", "https://"+hostname+":8443"),
	}
	if c.databaseURL == "" {
		return c, errors.New("HAETAE_DATABASE_URL is required")
	}
	names := []string{"localhost", "127.0.0.1"}
	if hostname != "" {
		names = append(names, hostname)
	}
	if h := urlHost(c.gatewayURL); h != "" {
		names = append(names, h)
	}
	for _, n := range strings.Split(os.Getenv("HAETAE_GATEWAY_NAMES"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	c.gatewayNames = names
	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func urlHost(u string) string {
	rest, ok := strings.CutPrefix(u, "https://")
	if !ok {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.HasSuffix(host, "]") {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String("haetae-server"))
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch args := flag.Args(); {
	case len(args) == 1 && args[0] == "serve":
		err = serve(ctx, log)
	case len(args) >= 2 && args[0] == "token" && args[1] == "create":
		err = tokenCreate(ctx, args[2:])
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, log *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ca, err := pki.LoadOrCreateCA(cfg.dataDir)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.ResetConnections(ctx); err != nil {
		return err
	}
	if _, err := st.DefaultOrg(ctx); err != nil {
		return err
	}
	// Partitions must exist before the first agent sends metrics.
	if err := st.EnsurePartitions(ctx, time.Now()); err != nil {
		return err
	}
	go server.MaintainMetrics(ctx, st, time.Minute, log.With("component", "metrics"))

	gw := gateway.New(st, ca, gateway.Config{}, log)
	gs, err := server.ListenGateway(cfg.gatewayAddr, gw, ca, cfg.gatewayNames)
	if err != nil {
		return err
	}
	log.Info("agent gateway listening", "addr", gs.Addr(), "url", cfg.gatewayURL, "ca_pin", pki.Fingerprint(ca.DER))

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		return gs.Close()
	case err := <-gs.Done():
		return err
	}
}

func tokenCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	name := fs.String("name", "", "name the host will have, e.g. hel1-prod")
	ttl := fs.Duration("ttl", 24*time.Hour, "how long the token stays valid")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ca, err := pki.LoadOrCreateCA(cfg.dataDir)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	org, err := st.DefaultOrg(ctx)
	if err != nil {
		return err
	}
	token, _, err := st.CreateEnrollmentToken(ctx, org.ID, *name, *ttl)
	if err != nil {
		return err
	}

	fmt.Printf("Token for %s (valid %s, single use):\n  %s\n\nOn the host, run:\n  haetae-agent enroll --server %s --token %s --ca-pin %s\n",
		*name, *ttl, token, cfg.gatewayURL, token, pki.Fingerprint(ca.DER))
	return nil
}
