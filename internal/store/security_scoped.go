package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SeveritySummary is critical/high/medium/low counts for the security API.
type SeveritySummary struct {
	Critical int
	High     int
	Medium   int
	Low      int
}

// ContainerScanView is the vulnerability panel for one container (API / UI).
type ContainerScanView struct {
	ContainerID string
	Name        string
	Image       string
	ImageID     string
	ScanStatus  string // ok|failed|timeout|oom|unsupported|pending|none
	ScanError   string
	ScanAt      *time.Time
	ToolVersion string
	MatchStatus string // ok|failed|pending|none (none = never matched)
	MatchError  string
	DBBuiltAt   *time.Time
	Counts      SeverityCounts
	Findings    []Finding
}

// HostVulnSummary is a compact badge view for the hosts list.
type HostVulnSummary struct {
	Critical   int
	High       int
	Medium     int
	Low        int
	ScanFailed bool
	// Pending counts containers whose scan succeeded but whose image is not
	// matched yet (no match row or pending). Never shown as "no vulnerabilities".
	Pending int
}

// HTTPSOnlyURLs keeps only https:// URLs (scheme exactly https).
func HTTPSOnlyURLs(urls []string) []string {
	out := make([]string, 0, len(urls))
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			continue
		}
		out = append(out, u.String())
	}
	return out
}

// MisconfigKindLabel returns a plain-words label for a misconfig kind.
func MisconfigKindLabel(kind string) string {
	switch kind {
	case "published_port":
		return "published port"
	case "docker_socket":
		return "Docker socket mounted"
	case "root_user":
		return "runs as root"
	case "privileged":
		return "privileged"
	case "host_network":
		return "host network"
	case "host_pid":
		return "host PID namespace"
	case "dangerous_capability":
		return "dangerous capability"
	case "cert_expiring":
		return "certificate expiring"
	default:
		return kind
	}
}

// ImageUpdateDecisionLabel returns plain-words labels for update decisions.
func ImageUpdateDecisionLabel(decision string) string {
	switch decision {
	case "current":
		return "up to date"
	case "wait":
		return "waiting 48 h"
	case "eligible":
		return "eligible"
	case "eligible_critical_fix":
		return "eligible because it fixes a critical CVE"
	case "unknown":
		return "unknown"
	default:
		return decision
	}
}

// GetMatchState returns the match row for an image, or empty with status "none".
func (s *Store) GetMatchState(ctx context.Context, orgID uuid.UUID, imageID string) (ImageMatchState, error) {
	var m ImageMatchState
	err := s.pool.QueryRow(ctx, `
		SELECT org_id, image_id, status, error, db_built_at, matched_at
		FROM image_match_state WHERE org_id = $1 AND image_id = $2`,
		orgID, imageID,
	).Scan(&m.OrgID, &m.ImageID, &m.Status, &m.Error, &m.DBBuiltAt, &m.MatchedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ImageMatchState{OrgID: orgID, ImageID: imageID, Status: "none"}, nil
		}
		return ImageMatchState{}, fmt.Errorf("store: GetMatchState: %w", err)
	}
	return m, nil
}

// LatestScanForContainer returns the latest scan row for a container, or ErrNotFound.
func (s *Store) LatestScanForContainer(ctx context.Context, orgID, hostID uuid.UUID, containerID string) (Scan, error) {
	var sc Scan
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, host_id, container_id, image, image_id, trigger, status, error,
			tool_version, package_count, peak_memory_bytes, source_ref, created_at, started_at, finished_at
		FROM scans
		WHERE org_id = $1 AND host_id = $2 AND container_id = $3
		ORDER BY COALESCE(finished_at, created_at) DESC
		LIMIT 1`,
		orgID, hostID, containerID,
	).Scan(
		&sc.ID, &sc.OrgID, &sc.HostID, &sc.ContainerID, &sc.Image, &sc.ImageID, &sc.Trigger, &sc.Status, &sc.Error,
		&sc.ToolVersion, &sc.PackageCount, &sc.PeakMemoryBytes, &sc.SourceRef, &sc.CreatedAt, &sc.StartedAt, &sc.FinishedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Scan{}, ErrNotFound
		}
		return Scan{}, fmt.Errorf("store: LatestScanForContainer: %w", err)
	}
	return sc, nil
}

// ListFindingsForImage returns findings ordered by severity (critical first), capped at limit.
func (s *Store) ListFindingsForImage(ctx context.Context, orgID uuid.UUID, imageID string, limit int) ([]Finding, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT org_id, image_id, vuln_id, package, version, pkg_type, fixed_version,
			severity, description, urls, first_seen, last_seen
		FROM image_findings
		WHERE org_id = $1 AND image_id = $2
		ORDER BY CASE severity
			WHEN 'critical' THEN 0
			WHEN 'high' THEN 1
			WHEN 'medium' THEN 2
			WHEN 'low' THEN 3
			WHEN 'negligible' THEN 4
			ELSE 5
		END, vuln_id
		LIMIT $3`,
		orgID, imageID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: ListFindingsForImage: %w", err)
	}
	defer rows.Close()
	findings, err := scanFindings(rows)
	if err != nil {
		return nil, fmt.Errorf("store: ListFindingsForImage: %w", err)
	}
	for i := range findings {
		findings[i].URLs = HTTPSOnlyURLs(findings[i].URLs)
	}
	return findings, nil
}

// HostSecurityOpenSummary counts open misconfigs by severity for a host.
func (s *Store) HostSecurityOpenSummary(ctx context.Context, orgID, hostID uuid.UUID) (SeveritySummary, error) {
	var sum SeveritySummary
	err := s.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE severity = 'critical'),
			count(*) FILTER (WHERE severity = 'high'),
			count(*) FILTER (WHERE severity = 'medium'),
			count(*) FILTER (WHERE severity = 'low')
		FROM misconfigs
		WHERE org_id = $1 AND host_id = $2 AND resolved_at IS NULL`,
		orgID, hostID,
	).Scan(&sum.Critical, &sum.High, &sum.Medium, &sum.Low)
	if err != nil {
		return SeveritySummary{}, fmt.Errorf("store: HostSecurityOpenSummary: %w", err)
	}
	return sum, nil
}

// ContainerVulnViews builds the per-container vulnerability payload for a host.
// A failed/timeout/oom/unsupported scan or failed match never returns findings
// as a clean zero-vuln success — status carries the failure.
func (s *Store) ContainerVulnViews(ctx context.Context, orgID, hostID uuid.UUID) ([]ContainerScanView, error) {
	cs, err := s.ListContainers(ctx, orgID, hostID)
	if err != nil {
		return nil, err
	}
	out := make([]ContainerScanView, 0, len(cs))
	for _, c := range cs {
		v := ContainerScanView{
			ContainerID: c.ContainerID,
			Name:        c.Name,
			Image:       c.Image,
			ScanStatus:  "none",
			MatchStatus: "none",
			Findings:    []Finding{},
		}
		sc, err := s.LatestScanForContainer(ctx, orgID, hostID, c.ContainerID)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		v.ScanStatus = sc.Status
		v.ScanError = sc.Error
		v.ScanAt = sc.FinishedAt
		if v.ScanAt == nil {
			t := sc.CreatedAt
			v.ScanAt = &t
		}
		v.ToolVersion = sc.ToolVersion
		v.ImageID = sc.ImageID
		if sc.Image != "" {
			v.Image = sc.Image
		}

		if sc.ImageID != "" {
			ms, err := s.GetMatchState(ctx, orgID, sc.ImageID)
			if err != nil {
				return nil, err
			}
			if ms.Status == "" || ms.Status == "none" {
				v.MatchStatus = "none"
			} else {
				v.MatchStatus = ms.Status
				v.MatchError = ms.Error
				v.DBBuiltAt = ms.DBBuiltAt
			}
		}

		// Only surface findings when the latest scan and match both succeeded.
		// Failed/timeout/oom/unsupported must never look like "0 vulnerabilities".
		if sc.Status == ScanOK && v.MatchStatus == MatchOK && sc.ImageID != "" {
			counts, err := s.CountsBySeverity(ctx, orgID, sc.ImageID)
			if err != nil {
				return nil, err
			}
			v.Counts = counts
			findings, err := s.ListFindingsForImage(ctx, orgID, sc.ImageID, 200)
			if err != nil {
				return nil, err
			}
			v.Findings = findings
		}
		out = append(out, v)
	}
	return out, nil
}

// HostVulnBadgeSummary aggregates counts, whether any scan failed, and how many
// scanned containers still wait for matching (Pending).
func (s *Store) HostVulnBadgeSummary(ctx context.Context, orgID, hostID uuid.UUID) (HostVulnSummary, error) {
	views, err := s.ContainerVulnViews(ctx, orgID, hostID)
	if err != nil {
		return HostVulnSummary{}, err
	}
	var sum HostVulnSummary
	for _, v := range views {
		if scanFailedStatus(v.ScanStatus) || v.MatchStatus == MatchFailed {
			sum.ScanFailed = true
			continue
		}
		if v.ScanStatus == ScanOK && v.MatchStatus == MatchOK {
			sum.Critical += v.Counts.Critical
			sum.High += v.Counts.High
			sum.Medium += v.Counts.Medium
			sum.Low += v.Counts.Low
		} else if v.ScanStatus == ScanOK {
			// Scan ok but not matched yet: unknown, not clean.
			sum.Pending++
		}
	}
	return sum, nil
}

func scanFailedStatus(status string) bool {
	switch status {
	case ScanFailed, ScanTimeout, ScanOOM, ScanUnsupported:
		return true
	}
	return false
}

// SortFindingsBySeverity sorts findings critical→unknown in place (tests / helpers).
func SortFindingsBySeverity(fs []Finding) {
	rank := func(s string) int {
		switch s {
		case FindingCritical:
			return 0
		case FindingHigh:
			return 1
		case FindingMedium:
			return 2
		case FindingLow:
			return 3
		case FindingNegligible:
			return 4
		default:
			return 5
		}
	}
	sort.SliceStable(fs, func(i, j int) bool {
		ri, rj := rank(fs[i].Severity), rank(fs[j].Severity)
		if ri != rj {
			return ri < rj
		}
		return strings.Compare(fs[i].VulnID, fs[j].VulnID) < 0
	})
}
