package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Scan / SBOM field limits (agent reports are untrusted).
const (
	MaxSbomPackages       = 20000
	MaxVulnStringBytes    = 512
	MaxVulnDescBytes      = 4 << 10
	MaxVulnErrorBytes     = 2 << 10
	MaxScansPerContainer  = 30
	MaxVulnURLsPerFinding = 16
)

// Scan statuses (match 0009_security.sql).
const (
	ScanPending     = "pending"
	ScanOK          = "ok"
	ScanFailed      = "failed"
	ScanTimeout     = "timeout"
	ScanOOM         = "oom"
	ScanUnsupported = "unsupported"
)

// Match statuses.
const (
	MatchOK     = "ok"
	MatchFailed = "failed"
)

// Vuln DB statuses.
const (
	VulnDBNone     = "none"
	VulnDBOK       = "ok"
	VulnDBFailed   = "failed"
	VulnDBDisabled = "disabled"
)

// Finding severities (match 0009 check).
const (
	FindingUnknown    = "unknown"
	FindingNegligible = "negligible"
	FindingLow        = "low"
	FindingMedium     = "medium"
	FindingHigh       = "high"
	FindingCritical   = "critical"
)

// Valid scan triggers.
var validScanTriggers = map[string]bool{
	"start": true, "deploy": true, "daily": true, "forced": true, "candidate": true,
}

var validScanStatuses = map[string]bool{
	ScanPending: true, ScanOK: true, ScanFailed: true,
	ScanTimeout: true, ScanOOM: true, ScanUnsupported: true,
}

var validFindingSeverities = map[string]bool{
	FindingUnknown: true, FindingNegligible: true, FindingLow: true,
	FindingMedium: true, FindingHigh: true, FindingCritical: true,
}

// Scan is one inventory attempt for a container or candidate image.
type Scan struct {
	ID              uuid.UUID
	OrgID           uuid.UUID
	HostID          uuid.UUID
	ContainerID     string
	Image           string
	ImageID         string
	Trigger         string
	Status          string
	Error           string
	ToolVersion     string
	PackageCount    int
	PeakMemoryBytes int64
	SourceRef       string
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// SbomPackage is one package row from an inventory.
type SbomPackage struct {
	Name    string
	Version string
	Type    string
	PURL    string
}

// Sbom is the stored software bill of materials for one image id.
type Sbom struct {
	OrgID         uuid.UUID
	ImageID       string
	DistroName    string
	DistroVersion string
	DistroIDLike  string
	PackageCount  int
	LastScanID    *uuid.UUID
	ScannedAt     time.Time
	Packages      []SbomPackage
}

// Finding is one vulnerability match on an image.
type Finding struct {
	OrgID        uuid.UUID
	ImageID      string
	VulnID       string
	Package      string
	Version      string
	PkgType      string
	FixedVersion string
	Severity     string
	Description  string
	URLs         []string
	FirstSeen    time.Time
	LastSeen     time.Time
}

// ImageMatchState records the last match attempt for an SBOM.
type ImageMatchState struct {
	OrgID     uuid.UUID
	ImageID   string
	Status    string
	Error     string
	DBBuiltAt *time.Time
	MatchedAt time.Time
}

// VulnDB is the single-row vulnerability database state.
type VulnDB struct {
	SourceURL string
	BuiltAt   *time.Time
	CheckedAt *time.Time
	UpdatedAt *time.Time
	Status    string
	Error     string
}

// ScanStatusView is the latest scan outcome for a container (API / UI).
type ScanStatusView struct {
	ContainerID string
	Status      string // ok, failed, timeout, oom, unsupported, pending; or "none"
	Error       string
	FinishedAt  *time.Time
	ImageID     string
	MatchStatus string // ok, failed, or empty
	MatchError  string
}

// SeverityCounts aggregates findings by severity.
type SeverityCounts struct {
	Unknown    int
	Negligible int
	Low        int
	Medium     int
	High       int
	Critical   int
}

// ContainerFinding links a high/critical finding to a running container.
type ContainerFinding struct {
	HostID      uuid.UUID
	HostName    string
	ContainerID string
	Name        string
	Image       string
	ImageID     string
	StartedAt   *time.Time
	Finding     Finding
}

// SaveSbomInput is the validated payload for a successful inventory.
type SaveSbomInput struct {
	HostID          uuid.UUID
	ContainerID     string
	Image           string
	ImageID         string
	Trigger         string
	ToolVersion     string
	PeakMemoryBytes int64
	SourceRef       string
	StartedAt       *time.Time
	FinishedAt      *time.Time
	DistroName      string
	DistroVersion   string
	DistroIDLike    string
	Packages        []SbomPackage
}

// FailedScanInput records a non-ok inventory without touching findings.
type FailedScanInput struct {
	HostID          uuid.UUID
	ContainerID     string
	Image           string
	ImageID         string
	Trigger         string
	Status          string // failed, timeout, oom, unsupported
	Error           string
	ToolVersion     string
	PeakMemoryBytes int64
	SourceRef       string
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// ErrInvalidSbom is returned for validation failures on SaveSbom / RecordFailedScan.
var ErrInvalidSbom = errors.New("store: invalid sbom report")

// ErrForeignContainer is returned when a container_id is not on the session host.
var ErrForeignContainer = errors.New("store: container does not belong to host")

func clipVuln(s string, max int) string {
	if max <= 0 || len(s) <= max {
		if utf8.ValidString(s) {
			return s
		}
	}
	if len(s) > max {
		s = s[:max]
	}
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func validateTrigger(t string) error {
	if !validScanTriggers[t] {
		return fmt.Errorf("%w: trigger %q", ErrInvalidSbom, t)
	}
	return nil
}

// ContainerBelongsToHost reports whether containerID is a current container on hostID in orgID.
func (s *Store) ContainerBelongsToHost(ctx context.Context, orgID, hostID uuid.UUID, containerID string) (bool, error) {
	if containerID == "" {
		return true, nil // candidate scans have no container
	}
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM containers
		WHERE org_id = $1 AND host_id = $2 AND container_id = $3 AND removed_at IS NULL`,
		orgID, hostID, containerID,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: ContainerBelongsToHost: %w", err)
	}
	return n > 0, nil
}

// SaveSbom writes a successful scan, replaces the SBOM and packages for the image,
// and prunes old scans. Findings are not touched. All work is one transaction.
func (s *Store) SaveSbom(ctx context.Context, orgID uuid.UUID, in SaveSbomInput) (Scan, error) {
	if in.ImageID == "" {
		return Scan{}, fmt.Errorf("%w: empty image_id", ErrInvalidSbom)
	}
	if err := validateTrigger(in.Trigger); err != nil {
		return Scan{}, err
	}
	if len(in.Packages) > MaxSbomPackages {
		return Scan{}, fmt.Errorf("%w: package count %d exceeds %d", ErrInvalidSbom, len(in.Packages), MaxSbomPackages)
	}
	if in.ContainerID != "" {
		ok, err := s.ContainerBelongsToHost(ctx, orgID, in.HostID, in.ContainerID)
		if err != nil {
			return Scan{}, err
		}
		if !ok {
			return Scan{}, ErrForeignContainer
		}
	}

	in.Image = clipVuln(in.Image, MaxVulnStringBytes)
	in.ImageID = clipVuln(in.ImageID, MaxVulnStringBytes)
	in.ContainerID = clipVuln(in.ContainerID, MaxVulnStringBytes)
	in.ToolVersion = clipVuln(in.ToolVersion, MaxVulnStringBytes)
	in.SourceRef = clipVuln(in.SourceRef, MaxVulnStringBytes)
	in.DistroName = clipVuln(in.DistroName, MaxVulnStringBytes)
	in.DistroVersion = clipVuln(in.DistroVersion, MaxVulnStringBytes)
	in.DistroIDLike = clipVuln(in.DistroIDLike, MaxVulnStringBytes)

	pkgs := make([]SbomPackage, 0, len(in.Packages))
	for _, p := range in.Packages {
		pkgs = append(pkgs, SbomPackage{
			Name:    clipVuln(p.Name, MaxVulnStringBytes),
			Version: clipVuln(p.Version, MaxVulnStringBytes),
			Type:    clipVuln(p.Type, MaxVulnStringBytes),
			PURL:    clipVuln(p.PURL, MaxVulnStringBytes),
		})
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Scan{}, fmt.Errorf("store: SaveSbom: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var hostOrg uuid.UUID
	err = tx.QueryRow(ctx, `SELECT org_id FROM hosts WHERE id = $1`, in.HostID).Scan(&hostOrg)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Scan{}, fmt.Errorf("%w: unknown host", ErrInvalidSbom)
		}
		return Scan{}, fmt.Errorf("store: SaveSbom: %w", err)
	}
	if hostOrg != orgID {
		return Scan{}, fmt.Errorf("%w: host not in org", ErrInvalidSbom)
	}

	var sc Scan
	err = tx.QueryRow(ctx, `
		INSERT INTO scans (
			org_id, host_id, container_id, image, image_id, trigger, status, error,
			tool_version, package_count, peak_memory_bytes, source_ref, started_at, finished_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,'',$8,$9,$10,$11,$12,$13
		)
		RETURNING id, org_id, host_id, container_id, image, image_id, trigger, status, error,
			tool_version, package_count, peak_memory_bytes, source_ref, created_at, started_at, finished_at`,
		orgID, in.HostID, in.ContainerID, in.Image, in.ImageID, in.Trigger, ScanOK,
		in.ToolVersion, len(pkgs), in.PeakMemoryBytes, in.SourceRef, in.StartedAt, in.FinishedAt,
	).Scan(
		&sc.ID, &sc.OrgID, &sc.HostID, &sc.ContainerID, &sc.Image, &sc.ImageID, &sc.Trigger, &sc.Status, &sc.Error,
		&sc.ToolVersion, &sc.PackageCount, &sc.PeakMemoryBytes, &sc.SourceRef, &sc.CreatedAt, &sc.StartedAt, &sc.FinishedAt,
	)
	if err != nil {
		return Scan{}, fmt.Errorf("store: SaveSbom insert scan: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO sboms (org_id, image_id, distro_name, distro_version, distro_id_like, package_count, last_scan_id, scanned_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT (org_id, image_id) DO UPDATE SET
			distro_name = EXCLUDED.distro_name,
			distro_version = EXCLUDED.distro_version,
			distro_id_like = EXCLUDED.distro_id_like,
			package_count = EXCLUDED.package_count,
			last_scan_id = EXCLUDED.last_scan_id,
			scanned_at = now()`,
		orgID, in.ImageID, in.DistroName, in.DistroVersion, in.DistroIDLike, len(pkgs), sc.ID,
	)
	if err != nil {
		return Scan{}, fmt.Errorf("store: SaveSbom upsert sbom: %w", err)
	}

	_, err = tx.Exec(ctx, `DELETE FROM sbom_packages WHERE org_id = $1 AND image_id = $2`, orgID, in.ImageID)
	if err != nil {
		return Scan{}, fmt.Errorf("store: SaveSbom clear packages: %w", err)
	}
	for _, p := range pkgs {
		if p.Name == "" {
			continue
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO sbom_packages (org_id, image_id, name, version, type, purl)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT DO NOTHING`,
			orgID, in.ImageID, p.Name, p.Version, p.Type, p.PURL,
		)
		if err != nil {
			return Scan{}, fmt.Errorf("store: SaveSbom insert package: %w", err)
		}
	}

	if err := pruneScansTx(ctx, tx, orgID, in.HostID, in.ContainerID); err != nil {
		return Scan{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Scan{}, fmt.Errorf("store: SaveSbom commit: %w", err)
	}
	return sc, nil
}

// RecordFailedScan inserts a failed/timeout/oom/unsupported scan and does not
// modify an existing good SBOM or its findings. The latest scan row is what
// ScanStatusForContainer uses to show "scan failed".
func (s *Store) RecordFailedScan(ctx context.Context, orgID uuid.UUID, in FailedScanInput) (Scan, error) {
	switch in.Status {
	case ScanFailed, ScanTimeout, ScanOOM, ScanUnsupported:
	default:
		return Scan{}, fmt.Errorf("%w: status %q", ErrInvalidSbom, in.Status)
	}
	if err := validateTrigger(in.Trigger); err != nil {
		return Scan{}, err
	}
	if in.Error == "" {
		return Scan{}, fmt.Errorf("%w: failed scan needs error", ErrInvalidSbom)
	}
	if in.ContainerID != "" {
		ok, err := s.ContainerBelongsToHost(ctx, orgID, in.HostID, in.ContainerID)
		if err != nil {
			return Scan{}, err
		}
		if !ok {
			return Scan{}, ErrForeignContainer
		}
	}

	in.Image = clipVuln(in.Image, MaxVulnStringBytes)
	in.ImageID = clipVuln(in.ImageID, MaxVulnStringBytes)
	in.ContainerID = clipVuln(in.ContainerID, MaxVulnStringBytes)
	in.ToolVersion = clipVuln(in.ToolVersion, MaxVulnStringBytes)
	in.SourceRef = clipVuln(in.SourceRef, MaxVulnStringBytes)
	in.Error = clipVuln(in.Error, MaxVulnErrorBytes)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Scan{}, fmt.Errorf("store: RecordFailedScan: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var hostOrg uuid.UUID
	err = tx.QueryRow(ctx, `SELECT org_id FROM hosts WHERE id = $1`, in.HostID).Scan(&hostOrg)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Scan{}, fmt.Errorf("%w: unknown host", ErrInvalidSbom)
		}
		return Scan{}, fmt.Errorf("store: RecordFailedScan: %w", err)
	}
	if hostOrg != orgID {
		return Scan{}, fmt.Errorf("%w: host not in org", ErrInvalidSbom)
	}

	var sc Scan
	err = tx.QueryRow(ctx, `
		INSERT INTO scans (
			org_id, host_id, container_id, image, image_id, trigger, status, error,
			tool_version, package_count, peak_memory_bytes, source_ref, started_at, finished_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,0,$10,$11,$12,$13
		)
		RETURNING id, org_id, host_id, container_id, image, image_id, trigger, status, error,
			tool_version, package_count, peak_memory_bytes, source_ref, created_at, started_at, finished_at`,
		orgID, in.HostID, in.ContainerID, in.Image, in.ImageID, in.Trigger, in.Status, in.Error,
		in.ToolVersion, in.PeakMemoryBytes, in.SourceRef, in.StartedAt, in.FinishedAt,
	).Scan(
		&sc.ID, &sc.OrgID, &sc.HostID, &sc.ContainerID, &sc.Image, &sc.ImageID, &sc.Trigger, &sc.Status, &sc.Error,
		&sc.ToolVersion, &sc.PackageCount, &sc.PeakMemoryBytes, &sc.SourceRef, &sc.CreatedAt, &sc.StartedAt, &sc.FinishedAt,
	)
	if err != nil {
		return Scan{}, fmt.Errorf("store: RecordFailedScan insert: %w", err)
	}

	if err := pruneScansTx(ctx, tx, orgID, in.HostID, in.ContainerID); err != nil {
		return Scan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Scan{}, fmt.Errorf("store: RecordFailedScan commit: %w", err)
	}
	return sc, nil
}

func pruneScansTx(ctx context.Context, tx pgx.Tx, orgID, hostID uuid.UUID, containerID string) error {
	// Keep the last MaxScansPerContainer per (host, container_id). Empty
	// container_id (candidates) are pruned the same way per host+''.
	_, err := tx.Exec(ctx, `
		DELETE FROM scans
		WHERE id IN (
			SELECT id FROM (
				SELECT id, row_number() OVER (
					PARTITION BY host_id, container_id ORDER BY created_at DESC
				) AS rn
				FROM scans
				WHERE org_id = $1 AND host_id = $2 AND container_id = $3
			) t
			WHERE rn > $4
		)`,
		orgID, hostID, containerID, MaxScansPerContainer,
	)
	if err != nil {
		return fmt.Errorf("store: prune scans: %w", err)
	}
	return nil
}

// LoadSbom returns the SBOM and packages for an image, or ErrNotFound.
func (s *Store) LoadSbom(ctx context.Context, orgID uuid.UUID, imageID string) (Sbom, error) {
	var sb Sbom
	err := s.pool.QueryRow(ctx, `
		SELECT org_id, image_id, distro_name, distro_version, distro_id_like, package_count, last_scan_id, scanned_at
		FROM sboms WHERE org_id = $1 AND image_id = $2`,
		orgID, imageID,
	).Scan(&sb.OrgID, &sb.ImageID, &sb.DistroName, &sb.DistroVersion, &sb.DistroIDLike, &sb.PackageCount, &sb.LastScanID, &sb.ScannedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Sbom{}, ErrNotFound
		}
		return Sbom{}, fmt.Errorf("store: LoadSbom: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT name, version, type, purl FROM sbom_packages
		WHERE org_id = $1 AND image_id = $2
		ORDER BY name, version, type`,
		orgID, imageID,
	)
	if err != nil {
		return Sbom{}, fmt.Errorf("store: LoadSbom packages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p SbomPackage
		if err := rows.Scan(&p.Name, &p.Version, &p.Type, &p.PURL); err != nil {
			return Sbom{}, fmt.Errorf("store: LoadSbom packages: %w", err)
		}
		sb.Packages = append(sb.Packages, p)
	}
	if err := rows.Err(); err != nil {
		return Sbom{}, fmt.Errorf("store: LoadSbom packages: %w", err)
	}
	if sb.Packages == nil {
		sb.Packages = []SbomPackage{}
	}
	return sb, nil
}

// ListSbomImageIDs returns image ids that have an SBOM, paged for rematch.
func (s *Store) ListSbomImageIDs(ctx context.Context, orgID uuid.UUID, after string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT image_id FROM sboms
		WHERE org_id = $1 AND image_id > $2
		ORDER BY image_id
		LIMIT $3`,
		orgID, after, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: ListSbomImageIDs: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: ListSbomImageIDs: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListAllSbomImageIDs pages through every org's SBOMs for rematch.
func (s *Store) ListAllSbomImageIDs(ctx context.Context, afterOrg uuid.UUID, afterImage string, limit int) ([]struct {
	OrgID   uuid.UUID
	ImageID string
}, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT org_id, image_id FROM sboms
		WHERE (org_id > $1) OR (org_id = $1 AND image_id > $2)
		ORDER BY org_id, image_id
		LIMIT $3`,
		afterOrg, afterImage, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: ListAllSbomImageIDs: %w", err)
	}
	defer rows.Close()
	var out []struct {
		OrgID   uuid.UUID
		ImageID string
	}
	for rows.Next() {
		var row struct {
			OrgID   uuid.UUID
			ImageID string
		}
		if err := rows.Scan(&row.OrgID, &row.ImageID); err != nil {
			return nil, fmt.Errorf("store: ListAllSbomImageIDs: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ReplaceFindings replaces all findings for an image in one transaction,
// keeping first_seen of unchanged (vuln_id, package, version) keys.
func (s *Store) ReplaceFindings(ctx context.Context, orgID uuid.UUID, imageID string, findings []Finding) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: ReplaceFindings: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM sboms WHERE org_id = $1 AND image_id = $2`, orgID, imageID).Scan(&n); err != nil {
		return fmt.Errorf("store: ReplaceFindings: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}

	type key struct{ v, p, ver string }
	keep := make(map[key]bool, len(findings))
	now := time.Now().UTC()
	for i := range findings {
		f := &findings[i]
		f.VulnID = clipVuln(f.VulnID, MaxVulnStringBytes)
		f.Package = clipVuln(f.Package, MaxVulnStringBytes)
		f.Version = clipVuln(f.Version, MaxVulnStringBytes)
		f.PkgType = clipVuln(f.PkgType, MaxVulnStringBytes)
		f.FixedVersion = clipVuln(f.FixedVersion, MaxVulnStringBytes)
		f.Description = clipVuln(f.Description, MaxVulnDescBytes)
		if !validFindingSeverities[f.Severity] {
			f.Severity = FindingUnknown
		}
		if len(f.URLs) > MaxVulnURLsPerFinding {
			f.URLs = f.URLs[:MaxVulnURLsPerFinding]
		}
		for j := range f.URLs {
			f.URLs[j] = clipVuln(f.URLs[j], MaxVulnStringBytes)
		}
		k := key{f.VulnID, f.Package, f.Version}
		keep[k] = true
		urls, err := json.Marshal(f.URLs)
		if err != nil {
			return fmt.Errorf("store: ReplaceFindings urls: %w", err)
		}
		if urls == nil {
			urls = []byte("[]")
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO image_findings (
				org_id, image_id, vuln_id, package, version, pkg_type, fixed_version,
				severity, description, urls, first_seen, last_seen
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
			ON CONFLICT (org_id, image_id, vuln_id, package, version) DO UPDATE SET
				pkg_type = EXCLUDED.pkg_type,
				fixed_version = EXCLUDED.fixed_version,
				severity = EXCLUDED.severity,
				description = EXCLUDED.description,
				urls = EXCLUDED.urls,
				last_seen = EXCLUDED.last_seen`,
			orgID, imageID, f.VulnID, f.Package, f.Version, f.PkgType, f.FixedVersion,
			f.Severity, f.Description, urls, now,
		)
		if err != nil {
			return fmt.Errorf("store: ReplaceFindings upsert: %w", err)
		}
	}

	rows, err := tx.Query(ctx, `
		SELECT vuln_id, package, version FROM image_findings
		WHERE org_id = $1 AND image_id = $2`, orgID, imageID)
	if err != nil {
		return fmt.Errorf("store: ReplaceFindings list: %w", err)
	}
	var gone []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.v, &k.p, &k.ver); err != nil {
			rows.Close()
			return fmt.Errorf("store: ReplaceFindings list: %w", err)
		}
		if !keep[k] {
			gone = append(gone, k)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: ReplaceFindings list: %w", err)
	}
	for _, k := range gone {
		_, err = tx.Exec(ctx, `
			DELETE FROM image_findings
			WHERE org_id = $1 AND image_id = $2 AND vuln_id = $3 AND package = $4 AND version = $5`,
			orgID, imageID, k.v, k.p, k.ver,
		)
		if err != nil {
			return fmt.Errorf("store: ReplaceFindings delete: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: ReplaceFindings commit: %w", err)
	}
	return nil
}

// SetMatchState upserts the match result for an image.
func (s *Store) SetMatchState(ctx context.Context, orgID uuid.UUID, imageID, status, errText string, dbBuiltAt *time.Time) error {
	if status != MatchOK && status != MatchFailed {
		return fmt.Errorf("store: SetMatchState: bad status %q", status)
	}
	errText = clipVuln(errText, MaxVulnErrorBytes)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO image_match_state (org_id, image_id, status, error, db_built_at, matched_at)
		VALUES ($1,$2,$3,$4,$5,now())
		ON CONFLICT (org_id, image_id) DO UPDATE SET
			status = EXCLUDED.status,
			error = EXCLUDED.error,
			db_built_at = EXCLUDED.db_built_at,
			matched_at = now()`,
		orgID, imageID, status, errText, dbBuiltAt,
	)
	if err != nil {
		return fmt.Errorf("store: SetMatchState: %w", err)
	}
	return nil
}

// GetVulnDB returns the single vuln_db row.
func (s *Store) GetVulnDB(ctx context.Context) (VulnDB, error) {
	var v VulnDB
	err := s.pool.QueryRow(ctx, `
		SELECT source_url, built_at, checked_at, updated_at, status, error
		FROM vuln_db WHERE id = 1`).Scan(
		&v.SourceURL, &v.BuiltAt, &v.CheckedAt, &v.UpdatedAt, &v.Status, &v.Error,
	)
	if err != nil {
		return VulnDB{}, fmt.Errorf("store: GetVulnDB: %w", err)
	}
	return v, nil
}

// UpdateVulnDB updates the single vuln_db row.
func (s *Store) UpdateVulnDB(ctx context.Context, v VulnDB) error {
	v.Error = clipVuln(v.Error, MaxVulnErrorBytes)
	v.SourceURL = clipVuln(v.SourceURL, MaxVulnStringBytes*4)
	switch v.Status {
	case VulnDBNone, VulnDBOK, VulnDBFailed, VulnDBDisabled:
	default:
		return fmt.Errorf("store: UpdateVulnDB: bad status %q", v.Status)
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE vuln_db SET
			source_url = $1, built_at = $2, checked_at = $3, updated_at = $4,
			status = $5, error = $6
		WHERE id = 1`,
		v.SourceURL, v.BuiltAt, v.CheckedAt, v.UpdatedAt, v.Status, v.Error,
	)
	if err != nil {
		return fmt.Errorf("store: UpdateVulnDB: %w", err)
	}
	return nil
}

// ListFindingsForHost returns findings for images used by the host's current containers.
func (s *Store) ListFindingsForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]Finding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.org_id, f.image_id, f.vuln_id, f.package, f.version, f.pkg_type, f.fixed_version,
			f.severity, f.description, f.urls, f.first_seen, f.last_seen
		FROM image_findings f
		WHERE f.org_id = $1 AND f.image_id IN (
			SELECT DISTINCT s.image_id FROM scans s
			WHERE s.org_id = $1 AND s.host_id = $2 AND s.status = $3 AND s.image_id <> ''
			  AND s.container_id IN (
				SELECT container_id FROM containers
				WHERE org_id = $1 AND host_id = $2 AND removed_at IS NULL
			  )
		)
		ORDER BY f.severity, f.vuln_id`,
		orgID, hostID, ScanOK,
	)
	if err != nil {
		return nil, fmt.Errorf("store: ListFindingsForHost: %w", err)
	}
	defer rows.Close()
	return scanFindings(rows)
}

// FindingsForContainer returns findings for the image from the container's latest ok scan.
func (s *Store) FindingsForContainer(ctx context.Context, orgID, hostID uuid.UUID, containerID string) ([]Finding, error) {
	var imageID string
	err := s.pool.QueryRow(ctx, `
		SELECT image_id FROM scans
		WHERE org_id = $1 AND host_id = $2 AND container_id = $3 AND status = $4 AND image_id <> ''
		ORDER BY COALESCE(finished_at, created_at) DESC
		LIMIT 1`,
		orgID, hostID, containerID, ScanOK,
	).Scan(&imageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return []Finding{}, nil
		}
		return nil, fmt.Errorf("store: FindingsForContainer: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT org_id, image_id, vuln_id, package, version, pkg_type, fixed_version,
			severity, description, urls, first_seen, last_seen
		FROM image_findings
		WHERE org_id = $1 AND image_id = $2
		ORDER BY severity, vuln_id`,
		orgID, imageID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: FindingsForContainer: %w", err)
	}
	defer rows.Close()
	return scanFindings(rows)
}

// CountsBySeverity returns finding counts for an image.
func (s *Store) CountsBySeverity(ctx context.Context, orgID uuid.UUID, imageID string) (SeverityCounts, error) {
	var c SeverityCounts
	err := s.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE severity = 'unknown'),
			count(*) FILTER (WHERE severity = 'negligible'),
			count(*) FILTER (WHERE severity = 'low'),
			count(*) FILTER (WHERE severity = 'medium'),
			count(*) FILTER (WHERE severity = 'high'),
			count(*) FILTER (WHERE severity = 'critical')
		FROM image_findings WHERE org_id = $1 AND image_id = $2`,
		orgID, imageID,
	).Scan(&c.Unknown, &c.Negligible, &c.Low, &c.Medium, &c.High, &c.Critical)
	if err != nil {
		return SeverityCounts{}, fmt.Errorf("store: CountsBySeverity: %w", err)
	}
	return c, nil
}

// ScanStatusForContainer returns the latest scan (any status) and match state for a container.
// A failed latest scan is reported as failed even when an older ok SBOM/findings exist.
func (s *Store) ScanStatusForContainer(ctx context.Context, orgID, hostID uuid.UUID, containerID string) (ScanStatusView, error) {
	var v ScanStatusView
	v.ContainerID = containerID
	v.Status = "none"
	err := s.pool.QueryRow(ctx, `
		SELECT status, error, finished_at, image_id FROM scans
		WHERE org_id = $1 AND host_id = $2 AND container_id = $3
		ORDER BY COALESCE(finished_at, created_at) DESC
		LIMIT 1`,
		orgID, hostID, containerID,
	).Scan(&v.Status, &v.Error, &v.FinishedAt, &v.ImageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			v.Status = "none"
			return v, nil
		}
		return ScanStatusView{}, fmt.Errorf("store: ScanStatusForContainer: %w", err)
	}
	if v.ImageID != "" {
		_ = s.pool.QueryRow(ctx, `
			SELECT status, error FROM image_match_state
			WHERE org_id = $1 AND image_id = $2`,
			orgID, v.ImageID,
		).Scan(&v.MatchStatus, &v.MatchError)
	}
	return v, nil
}

// NewCriticalHighFindingsForRunningContainers returns high/critical findings
// whose first_seen is at or after since, on images used by running containers.
func (s *Store) NewCriticalHighFindingsForRunningContainers(ctx context.Context, orgID uuid.UUID, since time.Time) ([]ContainerFinding, error) {
	rows, err := s.pool.Query(ctx, `
		WITH latest_ok AS (
			SELECT DISTINCT ON (host_id, container_id)
				host_id, container_id, image_id, image
			FROM scans
			WHERE org_id = $1 AND status = $2 AND image_id <> '' AND container_id <> ''
			ORDER BY host_id, container_id, COALESCE(finished_at, created_at) DESC
		)
		SELECT c.host_id, h.name, c.container_id, c.name, lo.image, lo.image_id, c.started_at,
			f.org_id, f.image_id, f.vuln_id, f.package, f.version, f.pkg_type, f.fixed_version,
			f.severity, f.description, f.urls, f.first_seen, f.last_seen
		FROM containers c
		JOIN hosts h ON h.id = c.host_id
		JOIN latest_ok lo ON lo.host_id = c.host_id AND lo.container_id = c.container_id
		JOIN image_findings f ON f.org_id = $1 AND f.image_id = lo.image_id
		WHERE c.org_id = $1 AND c.removed_at IS NULL AND lower(c.state) = 'running'
		  AND f.severity IN ('high', 'critical')
		  AND f.first_seen >= $3
		ORDER BY c.host_id, c.container_id, f.severity DESC, f.vuln_id`,
		orgID, ScanOK, since,
	)
	if err != nil {
		return nil, fmt.Errorf("store: NewCriticalHighFindingsForRunningContainers: %w", err)
	}
	defer rows.Close()
	var out []ContainerFinding
	for rows.Next() {
		var cf ContainerFinding
		var urls []byte
		if err := rows.Scan(
			&cf.HostID, &cf.HostName, &cf.ContainerID, &cf.Name, &cf.Image, &cf.ImageID, &cf.StartedAt,
			&cf.Finding.OrgID, &cf.Finding.ImageID, &cf.Finding.VulnID, &cf.Finding.Package, &cf.Finding.Version,
			&cf.Finding.PkgType, &cf.Finding.FixedVersion, &cf.Finding.Severity, &cf.Finding.Description,
			&urls, &cf.Finding.FirstSeen, &cf.Finding.LastSeen,
		); err != nil {
			return nil, fmt.Errorf("store: NewCriticalHighFindingsForRunningContainers: %w", err)
		}
		cf.Finding.URLs = decodeURLs(urls)
		out = append(out, cf)
	}
	return out, rows.Err()
}

// HighCriticalFindingsForRunningContainers returns all current high/critical
// findings on running containers (for alert resolve / fire, not only new ones).
func (s *Store) HighCriticalFindingsForRunningContainers(ctx context.Context, orgID uuid.UUID) ([]ContainerFinding, error) {
	rows, err := s.pool.Query(ctx, `
		WITH latest_ok AS (
			SELECT DISTINCT ON (host_id, container_id)
				host_id, container_id, image_id, image
			FROM scans
			WHERE org_id = $1 AND status = $2 AND image_id <> '' AND container_id <> ''
			ORDER BY host_id, container_id, COALESCE(finished_at, created_at) DESC
		)
		SELECT c.host_id, h.name, c.container_id, c.name, lo.image, lo.image_id, c.started_at,
			f.org_id, f.image_id, f.vuln_id, f.package, f.version, f.pkg_type, f.fixed_version,
			f.severity, f.description, f.urls, f.first_seen, f.last_seen
		FROM containers c
		JOIN hosts h ON h.id = c.host_id
		JOIN latest_ok lo ON lo.host_id = c.host_id AND lo.container_id = c.container_id
		JOIN image_findings f ON f.org_id = $1 AND f.image_id = lo.image_id
		WHERE c.org_id = $1 AND c.removed_at IS NULL AND lower(c.state) = 'running'
		  AND f.severity IN ('high', 'critical')
		ORDER BY c.host_id, c.container_id, f.severity DESC, f.vuln_id`,
		orgID, ScanOK,
	)
	if err != nil {
		return nil, fmt.Errorf("store: HighCriticalFindingsForRunningContainers: %w", err)
	}
	defer rows.Close()
	var out []ContainerFinding
	for rows.Next() {
		var cf ContainerFinding
		var urls []byte
		if err := rows.Scan(
			&cf.HostID, &cf.HostName, &cf.ContainerID, &cf.Name, &cf.Image, &cf.ImageID, &cf.StartedAt,
			&cf.Finding.OrgID, &cf.Finding.ImageID, &cf.Finding.VulnID, &cf.Finding.Package, &cf.Finding.Version,
			&cf.Finding.PkgType, &cf.Finding.FixedVersion, &cf.Finding.Severity, &cf.Finding.Description,
			&urls, &cf.Finding.FirstSeen, &cf.Finding.LastSeen,
		); err != nil {
			return nil, fmt.Errorf("store: HighCriticalFindingsForRunningContainers: %w", err)
		}
		cf.Finding.URLs = decodeURLs(urls)
		out = append(out, cf)
	}
	return out, rows.Err()
}

func scanFindings(rows pgx.Rows) ([]Finding, error) {
	var out []Finding
	for rows.Next() {
		var f Finding
		var urls []byte
		if err := rows.Scan(
			&f.OrgID, &f.ImageID, &f.VulnID, &f.Package, &f.Version, &f.PkgType, &f.FixedVersion,
			&f.Severity, &f.Description, &urls, &f.FirstSeen, &f.LastSeen,
		); err != nil {
			return nil, err
		}
		f.URLs = decodeURLs(urls)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Finding{}
	}
	return out, nil
}

func decodeURLs(b []byte) []string {
	if len(b) == 0 {
		return []string{}
	}
	var u []string
	if err := json.Unmarshal(b, &u); err != nil || u == nil {
		return []string{}
	}
	return u
}

// NormalizeFindingSeverity maps grype-ish severity strings into the DB enum.
func NormalizeFindingSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case FindingCritical:
		return FindingCritical
	case FindingHigh:
		return FindingHigh
	case FindingMedium:
		return FindingMedium
	case FindingLow:
		return FindingLow
	case FindingNegligible:
		return FindingNegligible
	default:
		return FindingUnknown
	}
}
