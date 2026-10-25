package scan

import (
	"fmt"
	"regexp"
	"strings"
)

// imageRefRe accepts Docker image ids and refs without shell metacharacters.
// Examples: sha256:abc..., alpine:3.20, registry.example.com/a/b@sha256:dead...
var imageRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/\-:@{}"=~+,]*$`)

func validateImageArg(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("empty image reference")
	}
	if len(ref) > 512 {
		return fmt.Errorf("image reference too long")
	}
	if strings.ContainsAny(ref, " \t\n\r;&|$`\\'<>()[]") {
		return fmt.Errorf("image reference contains forbidden characters")
	}
	if !imageRefRe.MatchString(ref) {
		return fmt.Errorf("invalid image reference")
	}
	return nil
}

func validateRegistryArg(ref string) error {
	if err := validateImageArg(ref); err != nil {
		return err
	}
	if !strings.Contains(ref, "@sha256:") {
		return fmt.Errorf("registry ref must include @sha256: digest")
	}
	return nil
}
