package server

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexseymer/haetae/internal/alerts"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/vuln"
)

// VulnLoopConfig is built from environment variables.
type VulnLoopConfig struct {
	ListingURL string
	DataDir    string
	CheckEvery time.Duration
}

// VulnConfigFromEnv reads HAETAE_VULN_DB_URL and HAETAE_VULN_DB_DIR.
// Empty URL disables downloads (S11). "off" also disables.
// HAETAE_VULN_CHECK_EVERY is test-only and ignored unless HAETAE_TEST_HOOKS=1.
func VulnConfigFromEnv(dataDir string) VulnLoopConfig {
	url := strings.TrimSpace(os.Getenv("HAETAE_VULN_DB_URL"))
	dir := strings.TrimSpace(os.Getenv("HAETAE_VULN_DB_DIR"))
	if dir == "" {
		dir = filepath.Join(dataDir, "vuln-db")
	}
	every := 10 * time.Minute
	if os.Getenv("HAETAE_TEST_HOOKS") == "1" {
		if v := strings.TrimSpace(os.Getenv("HAETAE_VULN_CHECK_EVERY")); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				every = d
			}
		}
	}
	return VulnLoopConfig{
		ListingURL: url,
		DataDir:    dir,
		CheckEvery: every,
	}
}

// RunVulnManager refreshes the Grype DB and rematches SBOMs until ctx ends.
// evaluator may be nil (no nudge).
func RunVulnManager(ctx context.Context, st *store.Store, cfg VulnLoopConfig, evaluator *alerts.Evaluator, log *slog.Logger) *vuln.Manager {
	if log == nil {
		log = slog.Default()
	}
	var nudge vuln.Nudge
	if evaluator != nil {
		nudge = evaluator.Nudge
	}
	mgr := vuln.NewManager(st, vuln.Config{
		ListingURL: cfg.ListingURL,
		DataDir:    cfg.DataDir,
		CheckEvery: cfg.CheckEvery,
	}, log, nudge)
	go mgr.Run(ctx)
	return mgr
}
