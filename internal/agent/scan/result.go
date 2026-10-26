package scan

import (
	"encoding/json"
	"strings"
)

// Status values for an inventory run (proto SbomReport.status).
const (
	StatusOK          = "ok"
	StatusFailed      = "failed"
	StatusTimeout     = "timeout"
	StatusOOM         = "oom"
	StatusUnsupported = "unsupported"
)

// Trigger values for SbomReport.trigger.
const (
	TriggerStart     = "start"
	TriggerDeploy    = "deploy"
	TriggerDaily     = "daily"
	TriggerForced    = "forced"
	TriggerCandidate = "candidate"
)

// HelperDocument mirrors the helper's JSON (one document in memory).
type HelperDocument struct {
	ToolVersion  string       `json:"toolVersion"`
	ImageID      string       `json:"imageId"`
	Distro       HelperDistro `json:"distro"`
	Packages     []HelperPkg  `json:"packages"`
	PeakRSSBytes int64        `json:"peakRssBytes"`
	StartedMs    int64        `json:"startedMs"`
	FinishedMs   int64        `json:"finishedMs"`
}

// HelperDistro is the helper distro object.
type HelperDistro struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	IDLike  string `json:"idLike"`
}

// HelperPkg is one helper package row.
type HelperPkg struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Type    string `json:"type"`
	PURL    string `json:"purl"`
}

// LaunchResult is the classified outcome of one helper invocation.
type LaunchResult struct {
	Status      string
	Error       string
	Limited     bool // false when cgroup was unavailable (setrlimit fallback)
	Doc         *HelperDocument
	PeakMemory  int64 // from cgroup memory.peak when available, else doc
	RawTooLarge bool
}

const maxHelperOutput = 8 << 20
const maxErrorLen = 512

func parseHelperJSON(raw []byte) (*HelperDocument, error) {
	var doc HelperDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func clipError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxErrorLen {
		return s
	}
	return s[:maxErrorLen]
}

func appendLimitedFalse(toolVersion string) string {
	if toolVersion == "" {
		return "limited=false"
	}
	if strings.Contains(toolVersion, "limited=false") {
		return toolVersion
	}
	return toolVersion + ";limited=false"
}
