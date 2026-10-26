package agent

import (
	"fmt"
	"log/slog"
	"sync"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// Autonomy levels, lowest to highest.
const (
	LevelReport  = "report"
	LevelSuggest = "suggest"
	LevelAct     = "act"
)

// Command classes. "user" commands are user- or setting-initiated and the
// operator cannot issue them; "operator" commands need an autonomy level.
const (
	ClassUser     = "user"
	ClassOperator = "operator"
)

func levelRank(l string) int {
	switch l {
	case LevelAct:
		return 2
	case LevelSuggest:
		return 1
	default:
		return 0 // report and anything unknown
	}
}

func normalizeLevel(l string) string {
	switch l {
	case LevelAct, LevelSuggest:
		return l
	default:
		return LevelReport
	}
}

// ParseMaxAutonomy validates the --max-autonomy flag value.
func ParseMaxAutonomy(s string) (string, error) {
	switch s {
	case LevelReport, LevelSuggest, LevelAct:
		return s, nil
	}
	return "", fmt.Errorf("invalid --max-autonomy %q: want report, suggest or act", s)
}

// Policy holds the autonomy level announced by the server and an optional
// local cap. The effective level is the lower of the two.
type Policy struct {
	mu     sync.Mutex
	server string
	cap    string
	log    *slog.Logger
}

// NewPolicy returns a policy with the given local cap (empty means act).
// An unknown cap is treated as report, the safe direction.
func NewPolicy(localCap string, log *slog.Logger) *Policy {
	if localCap == "" {
		localCap = LevelAct
	}
	if log == nil {
		log = slog.Default()
	}
	return &Policy{server: LevelReport, cap: normalizeLevel(localCap), log: log}
}

// SetServerLevel records the level announced by the server.
func (p *Policy) SetServerLevel(level string) {
	level = normalizeLevel(level)
	p.mu.Lock()
	before := p.effectiveLocked()
	p.server = level
	after := p.effectiveLocked()
	p.mu.Unlock()
	if before != after {
		p.log.Info("autonomy level changed", "from", before, "to", after, "announced", level, "local_cap", p.cap)
	}
}

func (p *Policy) effectiveLocked() string {
	if levelRank(p.cap) < levelRank(p.server) {
		return p.cap
	}
	return p.server
}

// Effective returns the lower of the announced level and the local cap.
func (p *Policy) Effective() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.effectiveLocked()
}

// commandClasses classifies every Command kind. A test enumerates the oneof
// so a new kind cannot be added without an entry here. Kinds absent from the
// table are refused.
var commandClasses = map[string]string{
	"ping":              ClassUser,
	"query_logs":        ClassUser,
	"deploy":            ClassUser,
	"remove_project":    ClassUser, // keeps volumes; user-initiated only
	"scan_container":    ClassUser,
	"scan_image":        ClassUser,
	"pull_image":        ClassUser,
	"restart_container": ClassOperator,
}

// commandKind returns the oneof case name of cmd, or "" if none is set.
func commandKind(cmd *agentv1.Command) string {
	fd := cmd.ProtoReflect().WhichOneof(cmd.ProtoReflect().Descriptor().Oneofs().ByName("kind"))
	if fd == nil {
		return ""
	}
	return string(fd.Name())
}

// Check returns "" if cmd is allowed at the current effective level, else
// the refusal text. Unknown kinds return "unsupported command".
func (p *Policy) Check(cmd *agentv1.Command) string {
	return checkAllowed(p.Effective(), cmd)
}

func checkAllowed(level string, cmd *agentv1.Command) string {
	kind := commandKind(cmd)
	class, ok := commandClasses[kind]
	if !ok {
		return "unsupported command"
	}
	if class == ClassUser {
		return ""
	}
	auth := cmd.GetAuthorization()
	authName := "unspecified"
	need := ""
	switch auth {
	case agentv1.Authorization_AUTHORIZATION_APPROVED:
		authName, need = "approved", LevelSuggest
	case agentv1.Authorization_AUTHORIZATION_AUTO:
		authName, need = "auto", LevelAct
	default:
		return "refused: operator command needs an authorization"
	}
	if levelRank(level) < levelRank(need) {
		return fmt.Sprintf("refused: autonomy level %s does not allow %s (%s)", level, kind, authName)
	}
	return ""
}
