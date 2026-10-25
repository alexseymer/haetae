package grypedb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/anchore/clio"
	v6 "github.com/anchore/grype/grype/db/v6"
	"github.com/anchore/grype/grype/db/v6/distribution"
	"github.com/anchore/grype/grype/db/v6/installation"
)

// ListingURL builds the latest.json URL from a base (with or without /latest.json).
func ListingURL(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, ".json") {
		return base
	}
	base = strings.TrimRight(base, "/")
	return fmt.Sprintf("%s/v%d/%s", base, v6.ModelVersion, distribution.LatestFileName)
}

// DistConfig returns a distribution client config pointed at listingURL
// (full latest.json URL or Anchore base). Empty listingURL disables updates.
func DistConfig(listingURL string) distribution.Config {
	cfg := distribution.DefaultConfig()
	cfg.ID = clio.Identification{Name: "haetae", Version: "dev"}
	cfg.LatestURL = listingURL
	cfg.RequireUpdateCheck = true
	cfg.CheckTimeout = 30 * time.Second
	cfg.UpdateTimeout = 5 * time.Minute
	return cfg
}

// InstallConfig returns an installation curator config under dataDir.
func InstallConfig(dataDir string) installation.Config {
	cfg := installation.DefaultConfig(clio.Identification{Name: "haetae", Version: "dev"})
	cfg.DBRootDir = dataDir
	cfg.ValidateAge = false
	cfg.ValidateChecksum = true
	cfg.MaxAllowedBuiltAge = 365 * 24 * time.Hour
	cfg.UpdateCheckMaxFrequency = 0 // always allow check; we rate-limit ourselves
	return cfg
}

// ActiveDBDir returns the versioned DB directory used by the curator.
func ActiveDBDir(dataDir string) string {
	return InstallConfig(dataDir).DBDirectoryPath()
}

// RefreshResult describes one listing check / possible download.
type RefreshResult struct {
	Updated   bool
	BuiltAt   time.Time
	CheckedAt time.Time
	Error     error
	Message   string
}

// Refresh checks the listing at listingURL and downloads a newer DB into dataDir.
// On failure the previous DB (if any) is left in place.
func Refresh(ctx context.Context, dataDir, listingURL string) RefreshResult {
	now := time.Now().UTC()
	res := RefreshResult{CheckedAt: now}
	if listingURL == "" || listingURL == "off" {
		res.Message = "disabled"
		return res
	}
	select {
	case <-ctx.Done():
		res.Error = ctx.Err()
		return res
	default:
	}

	client, err := distribution.NewClient(DistConfig(listingURL))
	if err != nil {
		res.Error = fmt.Errorf("grypedb: client: %w", err)
		return res
	}
	curator, err := installation.NewCurator(InstallConfig(dataDir), client)
	if err != nil {
		res.Error = fmt.Errorf("grypedb: curator: %w", err)
		return res
	}

	updated, err := curator.Update()
	st := curator.Status()
	if !st.Built.IsZero() {
		res.BuiltAt = st.Built.UTC()
	}
	if err != nil {
		res.Error = err
		return res
	}
	res.Updated = updated
	if updated {
		res.BuiltAt = time.Now().UTC()
		if !st.Built.IsZero() {
			res.BuiltAt = st.Built.UTC()
		}
	}
	return res
}

// FetchLatestDocument downloads and parses latest.json from listingURL
// without installing. Used by tests and status probes.
func FetchLatestDocument(ctx context.Context, listingURL string) (*distribution.LatestDocument, error) {
	u := ListingURL(listingURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("grypedb: listing status %d", resp.StatusCode)
	}
	return distribution.NewLatestFromReader(resp.Body)
}

// WriteLatestDocument writes a latest.json for archiveFile (on disk).
// relativePath is the path as hosted next to latest.json (defaults to basename).
func WriteLatestDocument(w io.Writer, archiveFile string, built time.Time, relativePath string) error {
	arch, err := distribution.NewArchive(archiveFile, built.UTC(), v6.ModelVersion, v6.Revision, v6.Addition)
	if err != nil {
		return err
	}
	if relativePath != "" {
		arch.Path = relativePath
	}
	doc := distribution.LatestDocument{
		Status:  distribution.LifecycleStatus,
		Archive: *arch,
	}
	return json.NewEncoder(w).Encode(doc)
}

// ResolveArchiveURL joins listing base with archive path (no checksum query).
func ResolveArchiveURL(listingURL, archivePath string) (string, error) {
	u, err := url.Parse(ListingURL(listingURL))
	if err != nil {
		return "", err
	}
	u.Path = path.Join(path.Dir(u.Path), path.Clean(archivePath))
	u.RawQuery = ""
	return u.String(), nil
}

// Ensure dataDir exists.
func EnsureDir(dataDir string) error {
	return os.MkdirAll(dataDir, 0o700)
}
