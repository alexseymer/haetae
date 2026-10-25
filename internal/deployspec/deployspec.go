// Package deployspec validates the fields of a deploy. The server validates
// them when a project is saved and the agent validates them again before it
// runs git or docker with them: both sides use these functions, so the rules
// cannot drift apart. Every function returns an error whose message is safe to
// show to the user.
package deployspec

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxInlineCompose is the largest inline compose file accepted.
const MaxInlineCompose = 256 << 10

var (
	projectRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	serviceRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	domainRE  = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	branchRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	commitRE  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ProjectName checks a project name: lower-case letters, digits and dashes,
// at most 40 characters, starting with a letter or digit.
func ProjectName(s string) error {
	if !projectRE.MatchString(s) {
		return errors.New("project name must be 1 to 40 characters: lower-case letters, digits and dashes, starting with a letter or digit")
	}
	return nil
}

// Domain checks a public host name such as app.example.com (no scheme, port,
// path, wildcard or IP address). Empty means no ingress and is valid.
func Domain(s string) error {
	if s == "" {
		return nil
	}
	if len(s) > 253 || !domainRE.MatchString(s) {
		return errors.New("domain must be a lower-case host name such as app.example.com")
	}
	return nil
}

// Service checks a compose service name.
func Service(s string) error {
	if !serviceRE.MatchString(s) {
		return errors.New("service name may contain letters, digits, dot, dash and underscore only")
	}
	return nil
}

// Port checks a TCP port number.
func Port(p uint32) error {
	if p < 1 || p > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

// GitURL checks a repository URL. Only https:// URLs without credentials are
// accepted (tokens are passed separately). With allowLocal, an absolute
// file:// URL is accepted too; the agent enables that only for tests.
func GitURL(s string, allowLocal bool) error {
	if len(s) == 0 || len(s) > 512 || strings.ContainsAny(s, " \t\r\n\x00\\'\"`$;|&<>(){}") {
		return errors.New("repository URL must be a plain https:// address")
	}
	u, err := url.Parse(s)
	if err != nil {
		return errors.New("repository URL is not valid")
	}
	if allowLocal && u.Scheme == "file" && u.Host == "" && strings.HasPrefix(u.Path, "/") && !strings.Contains(u.Path, "..") {
		return nil
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("repository URL must be https://host/path without credentials, query or fragment")
	}
	if strings.HasPrefix(u.Host, "-") || strings.Contains(u.Path, "..") {
		return errors.New("repository URL is not valid")
	}
	return nil
}

// GitRef checks a branch name or a 40-character commit id. A name that could
// be read as an option, or that git would reject, is refused.
func GitRef(s string) error {
	if commitRE.MatchString(s) {
		return nil
	}
	if !branchRE.MatchString(s) || strings.Contains(s, "..") || strings.Contains(s, "//") ||
		strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, ".lock") || strings.Contains(s, "@{") {
		return errors.New("ref must be a branch name or a 40-character commit id")
	}
	return nil
}

// IsCommit reports whether ref is a full commit id.
func IsCommit(ref string) bool { return commitRE.MatchString(ref) }

// ComposePath checks the path of the compose file inside the repository: a
// clean relative path ending in .yml or .yaml. Empty means "compose.yaml".
func ComposePath(s string) (string, error) {
	if s == "" {
		return "compose.yaml", nil
	}
	if len(s) > 128 || strings.ContainsAny(s, "\\\x00") || path.IsAbs(s) || path.Clean(s) != s ||
		s == ".." || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "-") {
		return "", errors.New("compose file must be a relative path inside the repository, such as deploy/compose.yaml")
	}
	if ext := path.Ext(s); ext != ".yml" && ext != ".yaml" {
		return "", errors.New("compose file must end in .yml or .yaml")
	}
	return s, nil
}

// InlineCompose checks pasted compose content: UTF-8 text without NUL bytes,
// not empty, at most MaxInlineCompose bytes.
func InlineCompose(s string) error {
	switch {
	case strings.TrimSpace(s) == "":
		return errors.New("compose file is empty")
	case len(s) > MaxInlineCompose:
		return fmt.Errorf("compose file is larger than %d KiB", MaxInlineCompose>>10)
	case !utf8.ValidString(s) || strings.ContainsRune(s, 0):
		return errors.New("compose file must be UTF-8 text")
	}
	return nil
}
