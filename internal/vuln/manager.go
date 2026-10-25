package vuln

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/vuln/grypedb"
)

// Store is the persistence surface the vulnerability manager needs.
type Store interface {
	GetVulnDB(ctx context.Context) (store.VulnDB, error)
	UpdateVulnDB(ctx context.Context, v store.VulnDB) error
	ListAllSbomImageIDs(ctx context.Context, afterOrg uuid.UUID, afterImage string, limit int) ([]struct {
		OrgID   uuid.UUID
		ImageID string
	}, error)
	LoadSbom(ctx context.Context, orgID uuid.UUID, imageID string) (store.Sbom, error)
	ReplaceFindings(ctx context.Context, orgID uuid.UUID, imageID string, findings []store.Finding) error
	SetMatchState(ctx context.Context, orgID uuid.UUID, imageID, status, errText string, dbBuiltAt *time.Time) error
}

// Config tunes the vulnerability database manager.
type Config struct {
	// ListingURL is HAETAE_VULN_DB_URL. Empty or "off" disables downloads.
	ListingURL string
	// DataDir is HAETAE_VULN_DB_DIR (installation root).
	DataDir string
	// CheckEvery defaults to 10 minutes.
	CheckEvery time.Duration
	// RematchConcurrency defaults to 2.
	RematchConcurrency int
	// Matcher factory; nil uses grypedb.OpenMatcher on the active DB dir.
	OpenMatcher func(dbDir string) (Matcher, error)
}

func (c Config) withDefaults() Config {
	if c.CheckEvery <= 0 {
		c.CheckEvery = 10 * time.Minute
	}
	if c.RematchConcurrency <= 0 {
		c.RematchConcurrency = 2
	}
	if c.OpenMatcher == nil {
		c.OpenMatcher = func(dbDir string) (Matcher, error) {
			return OpenGrypeMatcher(dbDir)
		}
	}
	return c
}

// Nudge is called after a successful rematch so alerts can evaluate soon.
type Nudge func()

// Manager refreshes the Grype DB and rematches stored SBOMs.
type Manager struct {
	st    Store
	cfg   Config
	log   *slog.Logger
	nudge Nudge

	mu      sync.Mutex
	matcher Matcher
	ready   bool
}

// NewManager constructs a Manager. A nil nudge is ignored.
func NewManager(st Store, cfg Config, log *slog.Logger, nudge Nudge) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{st: st, cfg: cfg.withDefaults(), log: log, nudge: nudge}
}

// Disabled reports whether vulnerability DB downloads are off.
func (m *Manager) Disabled() bool {
	u := strings.TrimSpace(m.cfg.ListingURL)
	return u == "" || strings.EqualFold(u, "off")
}

// Ready reports whether a matcher is loaded.
func (m *Manager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ready && m.matcher != nil
}

// Matcher returns the current matcher (may be nil).
func (m *Manager) Matcher() Matcher {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.matcher
}

// Run checks the listing every CheckEvery until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	if err := m.bootstrap(ctx); err != nil {
		m.log.Error("vuln db bootstrap", "err", err)
	}
	t := time.NewTicker(m.cfg.CheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.CheckOnce(ctx); err != nil {
				m.log.Error("vuln db check", "err", err)
			}
		}
	}
}

func (m *Manager) bootstrap(ctx context.Context) error {
	if m.Disabled() {
		now := time.Now().UTC()
		return m.st.UpdateVulnDB(ctx, store.VulnDB{
			SourceURL: m.cfg.ListingURL,
			CheckedAt: &now,
			Status:    store.VulnDBDisabled,
			Error:     "",
		})
	}
	if err := grypedb.EnsureDir(m.cfg.DataDir); err != nil {
		return err
	}
	// Try to open an existing DB before the first network check.
	if err := m.openActive(); err != nil {
		m.log.Info("no local vuln db yet", "err", err)
	}
	return m.CheckOnce(ctx)
}

// CheckOnce performs one listing check / download / rematch cycle.
func (m *Manager) CheckOnce(ctx context.Context) error {
	now := time.Now().UTC()
	if m.Disabled() {
		return m.st.UpdateVulnDB(ctx, store.VulnDB{
			SourceURL: m.cfg.ListingURL,
			CheckedAt: &now,
			Status:    store.VulnDBDisabled,
		})
	}

	prev, _ := m.st.GetVulnDB(ctx)
	res := grypedb.Refresh(ctx, m.cfg.DataDir, m.cfg.ListingURL)
	if res.Error != nil {
		m.log.Error("vuln db refresh failed; keeping previous", "err", res.Error)
		v := store.VulnDB{
			SourceURL: m.cfg.ListingURL,
			BuiltAt:   prev.BuiltAt,
			CheckedAt: &res.CheckedAt,
			UpdatedAt: prev.UpdatedAt,
			Status:    store.VulnDBFailed,
			Error:     res.Error.Error(),
		}
		_ = m.st.UpdateVulnDB(ctx, v)
		// Keep using previous matcher if we have one.
		return res.Error
	}

	built := res.BuiltAt
	if built.IsZero() && prev.BuiltAt != nil {
		built = *prev.BuiltAt
	}
	var builtPtr *time.Time
	if !built.IsZero() {
		builtPtr = &built
	}
	updatedAt := prev.UpdatedAt
	if res.Updated {
		updatedAt = &res.CheckedAt
		if err := m.openActive(); err != nil {
			v := store.VulnDB{
				SourceURL: m.cfg.ListingURL,
				BuiltAt:   prev.BuiltAt,
				CheckedAt: &res.CheckedAt,
				UpdatedAt: prev.UpdatedAt,
				Status:    store.VulnDBFailed,
				Error:     err.Error(),
			}
			_ = m.st.UpdateVulnDB(ctx, v)
			return err
		}
	} else if !m.Ready() {
		_ = m.openActive()
	}

	status := store.VulnDBOK
	if !m.Ready() {
		status = store.VulnDBNone
	}
	v := store.VulnDB{
		SourceURL: m.cfg.ListingURL,
		BuiltAt:   builtPtr,
		CheckedAt: &res.CheckedAt,
		UpdatedAt: updatedAt,
		Status:    status,
		Error:     "",
	}
	if err := m.st.UpdateVulnDB(ctx, v); err != nil {
		return err
	}
	if res.Updated {
		if err := m.RematchAll(ctx); err != nil {
			m.log.Error("rematch after db update", "err", err)
			return err
		}
		if m.nudge != nil {
			m.nudge()
		}
	}
	return nil
}

func (m *Manager) openActive() error {
	dir := grypedb.ActiveDBDir(m.cfg.DataDir)
	if _, err := os.Stat(filepath.Join(dir, "vulnerability.db")); err != nil {
		return err
	}
	matcher, err := m.cfg.OpenMatcher(dir)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.matcher != nil {
		if c, ok := m.matcher.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}
	m.matcher = matcher
	m.ready = true
	m.mu.Unlock()
	return nil
}

// MatchImage loads the SBOM, matches, and stores findings + match state.
func (m *Manager) MatchImage(ctx context.Context, orgID uuid.UUID, imageID string) error {
	matcher := m.Matcher()
	if matcher == nil {
		return fmt.Errorf("vuln: matcher not ready")
	}
	sb, err := m.st.LoadSbom(ctx, orgID, imageID)
	if err != nil {
		return err
	}
	findings, built, err := matcher.Match(ctx, FromStoreSbom(sb))
	if err != nil {
		_ = m.st.SetMatchState(ctx, orgID, imageID, store.MatchFailed, err.Error(), nil)
		return err
	}
	if err := m.st.ReplaceFindings(ctx, orgID, imageID, ToStoreFindings(findings)); err != nil {
		_ = m.st.SetMatchState(ctx, orgID, imageID, store.MatchFailed, err.Error(), nil)
		return err
	}
	return m.st.SetMatchState(ctx, orgID, imageID, store.MatchOK, "", &built)
}

// RematchAll rematches every stored SBOM with bounded concurrency.
func (m *Manager) RematchAll(ctx context.Context) error {
	if !m.Ready() {
		return fmt.Errorf("vuln: matcher not ready")
	}
	type job struct {
		OrgID   uuid.UUID
		ImageID string
	}
	jobs := make(chan job, m.cfg.RematchConcurrency*2)
	errCh := make(chan error, m.cfg.RematchConcurrency)
	var wg sync.WaitGroup
	workers := m.cfg.RematchConcurrency
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				if err := m.MatchImage(ctx, j.OrgID, j.ImageID); err != nil {
					m.log.Error("rematch", "org", j.OrgID, "image", j.ImageID, "err", err)
					select {
					case errCh <- err:
					default:
					}
				}
			}
		}()
	}

	var afterOrg uuid.UUID
	afterImage := ""
	for {
		page, err := m.st.ListAllSbomImageIDs(ctx, afterOrg, afterImage, 100)
		if err != nil {
			close(jobs)
			wg.Wait()
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, row := range page {
			select {
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				return ctx.Err()
			case jobs <- job{OrgID: row.OrgID, ImageID: row.ImageID}:
			}
			afterOrg, afterImage = row.OrgID, row.ImageID
		}
		if len(page) < 100 {
			break
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

// SetMatcherForTest installs a matcher without a DB (tests).
func (m *Manager) SetMatcherForTest(matcher Matcher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.matcher = matcher
	m.ready = matcher != nil
}
