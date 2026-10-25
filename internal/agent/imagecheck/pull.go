package imagecheck

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// digestRE is a strict sha256 digest.
var digestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// refRE allows registry/name:tag without shell metacharacters.
var refRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._\-/:]{0,255}$`)

// Pull handles a PullImage command: re-checks digest and age, then pulls
// ref@digest. It never restarts or removes containers.
func (c *Checker) Pull(ctx context.Context, cmd *agentv1.PullImage) error {
	if cmd == nil {
		return fmt.Errorf("pull: empty command")
	}
	ref := strings.TrimSpace(cmd.GetRef())
	digest := strings.TrimSpace(cmd.GetDigest())
	if !refRE.MatchString(ref) || strings.ContainsAny(ref, " \t\n;|&$`") {
		return fmt.Errorf("pull: invalid ref")
	}
	if !digestRE.MatchString(digest) {
		return fmt.Errorf("pull: invalid digest")
	}
	if isDigestPinned(ref) {
		return fmt.Errorf("pull: ref must not already be digest-pinned")
	}

	info, err := c.Registry.Lookup(ctx, ref)
	if err != nil {
		return fmt.Errorf("pull: re-check: %w", err)
	}
	if info.Digest != digest {
		return fmt.Errorf("pull: digest mismatch: registry has %s, expected %s", info.Digest, digest)
	}
	minAge := time.Duration(cmd.GetMinAgeHours()) * time.Hour
	if minAge <= 0 {
		minAge = 48 * time.Hour
	}
	if !cmd.GetAllowYoung() && !info.CreatedAt.IsZero() {
		age := c.Now().Sub(info.CreatedAt)
		if age < minAge {
			return fmt.Errorf("pull: image too young (%s < %s)", age.Round(time.Minute), minAge)
		}
	}

	pinned := ref
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		pinned = ref[:i]
	}
	pinned = pinned + "@" + digest
	if c.Docker == nil {
		return fmt.Errorf("pull: no docker client")
	}
	return c.Docker.PullImage(ctx, pinned)
}

// Handle runs PullImage commands; returns nil for other kinds.
func (c *Checker) Handle(ctx context.Context, cmd *agentv1.Command) *agentv1.CommandResult {
	p := cmd.GetPullImage()
	if p == nil {
		return nil
	}
	res := &agentv1.CommandResult{}
	if err := c.Pull(ctx, p); err != nil {
		res.Error = err.Error()
	}
	return res
}
