// Command haetae-server is the Haetae control plane.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/alexseymer/haetae/internal/api"
	"github.com/alexseymer/haetae/internal/auth"
	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/deploys"
	"github.com/alexseymer/haetae/internal/gateway"
	"github.com/alexseymer/haetae/internal/installer"
	"github.com/alexseymer/haetae/internal/pki"
	"github.com/alexseymer/haetae/internal/secrets"
	"github.com/alexseymer/haetae/internal/server"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/version"
	"github.com/alexseymer/haetae/internal/webui"
)

const usage = `usage:
  haetae-server serve                     run the control plane
  haetae-server setup-code                print the first-run setup code
  haetae-server token create --name NAME  create a single-use enrollment token
  haetae-server --version

Configuration (environment):
  HAETAE_DATABASE_URL    PostgreSQL URL (required)
  HAETAE_DATA_DIR        state directory, holds the agent CA (default /var/lib/haetae)
  HAETAE_DIST_DIR        directory of agent binaries served at /download/ (default /usr/share/haetae/dist)
  HAETAE_GATEWAY_ADDR    agent gateway listen address (default :8443)
  HAETAE_GATEWAY_URL     URL agents dial (default https://<hostname>:8443)
  HAETAE_GATEWAY_NAMES   extra comma-separated names for the gateway and web certificates
  HAETAE_WEB_ADDR        web interface listen address (default :8080)
  HAETAE_WEB_URL         URL users open (default https://<hostname>:8080)
  HAETAE_WEB_TLS_CERT    certificate file for the web interface (with HAETAE_WEB_TLS_KEY);
                         default: one issued by the server's own CA
  HAETAE_TRUSTED_PROXIES comma-separated addresses/CIDRs of reverse proxies whose X-Forwarded-For is trusted
  HAETAE_VULN_DB_URL     Grype DB listing base URL (empty or "off" = disabled; no outbound by default)
  HAETAE_VULN_DB_DIR     Grype DB directory (default $HAETAE_DATA_DIR/vuln-db)
`

type config struct {
	databaseURL    string
	dataDir        string
	gatewayAddr    string
	gatewayURL     string
	gatewayNames   []string
	webAddr        string
	webURL         string
	webCertFile    string
	webKeyFile     string
	trustedProxies []netip.Prefix
}

func loadConfig() (config, error) {
	hostname, _ := os.Hostname()
	c := config{
		databaseURL: os.Getenv("HAETAE_DATABASE_URL"),
		dataDir:     envOr("HAETAE_DATA_DIR", "/var/lib/haetae"),
		gatewayAddr: envOr("HAETAE_GATEWAY_ADDR", ":8443"),
		gatewayURL:  envOr("HAETAE_GATEWAY_URL", "https://"+hostname+":8443"),
		webAddr:     envOr("HAETAE_WEB_ADDR", ":8080"),
		webURL:      envOr("HAETAE_WEB_URL", "https://"+hostname+":8080"),
		webCertFile: os.Getenv("HAETAE_WEB_TLS_CERT"),
		webKeyFile:  os.Getenv("HAETAE_WEB_TLS_KEY"),
	}
	if (c.webCertFile == "") != (c.webKeyFile == "") {
		return c, errors.New("HAETAE_WEB_TLS_CERT and HAETAE_WEB_TLS_KEY must be set together")
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
	if h := urlHost(c.webURL); h != "" {
		names = append(names, h)
	}
	for _, n := range strings.Split(os.Getenv("HAETAE_GATEWAY_NAMES"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	c.gatewayNames = names
	if os.Getenv("HAETAE_TRUST_PROXY") != "" {
		return c, errors.New("HAETAE_TRUST_PROXY was replaced by HAETAE_TRUSTED_PROXIES (a list of proxy addresses or CIDRs); unset the old variable")
	}
	for _, p := range strings.Split(os.Getenv("HAETAE_TRUSTED_PROXIES"), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			addr, aerr := netip.ParseAddr(p)
			if aerr != nil {
				return c, fmt.Errorf("HAETAE_TRUSTED_PROXIES: %q is not an address or CIDR", p)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		c.trustedProxies = append(c.trustedProxies, prefix)
	}
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
	case len(args) == 1 && args[0] == "setup-code":
		err = printSetupCode()
	case len(args) == 1 && args[0] == "healthcheck":
		err = healthcheck()
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

// printSetupCode writes DATA_DIR/setup-code to stdout. The file is only present
// while first-run setup is still required.
func printSetupCode() error {
	dataDir := envOr("HAETAE_DATA_DIR", "/var/lib/haetae")
	b, err := os.ReadFile(filepath.Join(dataDir, "setup-code")) //nolint:gosec // G304: a fixed file name inside the operator's own data directory
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("no setup code: setup is already done")
		}
		return err
	}
	_, err = os.Stdout.Write(b)
	return err
}

// healthcheck probes the local web interface (used as the container HEALTHCHECK).
func healthcheck() error {
	addr := envOr("HAETAE_WEB_ADDR", ":8080")
	port := "8080"
	if i := strings.LastIndex(addr, ":"); i >= 0 && i+1 < len(addr) {
		port = addr[i+1:]
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // local probe of our own listener
		},
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
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
	st, err := openStore(ctx, cfg.databaseURL, log)
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

	box, err := secrets.Open(cfg.dataDir)
	if err != nil {
		return err
	}
	chatSvc, err := startChat(ctx, st, cfg.dataDir, log)
	if err != nil {
		return err
	}
	go server.RunBriefing(ctx, st, chatSvc, log.With("component", "briefing"))
	alertsEval := server.RunAlertsEvaluator(ctx, st, chatSvc, log.With("component", "alerts"))
	vulnCfg := server.VulnConfigFromEnv(cfg.dataDir)
	vulnMgr := server.RunVulnManager(ctx, st, vulnCfg, alertsEval, log.With("component", "vuln"))
	gw := gateway.New(st, ca, gateway.Config{}, log)
	matchWorker := gateway.NewMatchWorker(ctx, vulnMgr, log.With("component", "sbom-match"), 2)
	gw.SetSbomHandlers(st, matchWorker)
	deploySvc := &deploys.Service{Store: st, Sender: gw, Box: box}
	gw.SetDeploys(deploySvc)
	go server.RunUpdatesLoop(ctx, st, gw, log.With("component", "updates"))
	gs, err := server.ListenGateway(cfg.gatewayAddr, gw, ca, cfg.gatewayNames)
	if err != nil {
		return err
	}
	log.Info("agent gateway listening", "addr", gs.Addr(), "url", cfg.gatewayURL, "ca_pin", pki.Fingerprint(ca.DER))

	ws, err := startWeb(ctx, cfg, st, ca, log, box, deploySvc, gw, chatSvc)
	if err != nil {
		_ = gs.Close()
		return err
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		return errors.Join(ws.Close(), gs.Close())
	case err := <-gs.Done():
		_ = ws.Close()
		return err
	case err := <-ws.Done():
		_ = gs.Close()
		return err
	}
}

// startWeb starts the web interface and API. While no account exists it
// creates a one-time setup code, logs it and keeps it in DATA_DIR/setup-code
// (readable only by the server's user), so only someone with access to the
// server can create the first administrator.
func startWeb(ctx context.Context, cfg config, st *store.Store, ca *pki.CA, log *slog.Logger, box *secrets.Box, deploySvc *deploys.Service, gw *gateway.Gateway, chatSvc *chat.Service) (*server.WebServer, error) {
	var (
		cert   tls.Certificate
		webPin string
		err    error
	)
	if cfg.webCertFile != "" {
		if cert, err = tls.LoadX509KeyPair(cfg.webCertFile, cfg.webKeyFile); err != nil {
			return nil, fmt.Errorf("load web certificate: %w", err)
		}
	} else {
		if cert, err = server.LoadOrCreateWebCert(cfg.dataDir, ca, cfg.gatewayNames); err != nil {
			return nil, err
		}
		// Our own CA is not trusted by browsers or curl, so install commands
		// pin the key instead.
		if webPin, err = server.PublicKeyPin(cert); err != nil {
			return nil, err
		}
	}

	codeFile := filepath.Join(cfg.dataDir, "setup-code")
	var setupCode string
	required, err := st.SetupRequired(ctx)
	if err != nil {
		return nil, err
	}
	if required {
		if setupCode, err = auth.NewCode(12); err != nil {
			return nil, err
		}
		if err := os.WriteFile(codeFile, []byte(setupCode+"\n"), 0o600); err != nil {
			return nil, fmt.Errorf("write setup code: %w", err)
		}
		log.Info("SETUP REQUIRED: open the web interface and enter this setup code", "url", cfg.webURL, "setup_code", setupCode)
	} else {
		_ = os.Remove(codeFile)
	}

	a := api.New(api.Config{
		Store:          st,
		Log:            log.With("component", "api"),
		Version:        version.Version,
		SetupCode:      setupCode,
		WebURL:         cfg.webURL,
		GatewayURL:     cfg.gatewayURL,
		CAPin:          pki.Fingerprint(ca.DER),
		WebPin:         webPin,
		TrustedProxies: cfg.trustedProxies,
		Deploys:        deploySvc,
		Sender:         gw,
		Secrets:        box,
		RefreshWatch:   gw.RefreshWatch,
		Chat:           chatSvc,
		OnSetupDone: func() {
			_ = os.Remove(codeFile)
		},
	})
	distDir := envOr("HAETAE_DIST_DIR", "/usr/share/haetae/dist")
	inst := installer.New(distDir)
	ws, err := server.ListenWeb(cfg.webAddr, server.Routes(a.Handler(), inst, webui.Handler()), cert)
	if err != nil {
		return nil, err
	}
	log.Info("web interface listening", "addr", ws.Addr(), "url", cfg.webURL)
	return ws, nil
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
	st, err := openStore(ctx, cfg.databaseURL, slog.Default())
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

// openStore connects to PostgreSQL and applies migrations, waiting for the
// database to come up. In a container setup the server often starts at the
// same moment as the database; dying on the first refused connection would
// turn a few seconds of startup into a crash loop. It gives up after about
// two minutes, or when ctx ends.
func openStore(ctx context.Context, url string, log *slog.Logger) (*store.Store, error) {
	const attempts = 60
	var err error
	for i := 1; i <= attempts; i++ {
		var st *store.Store
		if st, err = store.Open(ctx, url); err == nil {
			return st, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		log.Warn("database not ready, retrying", "attempt", i, "of", attempts, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, fmt.Errorf("database still unavailable after %d attempts: %w", attempts, err)
}
