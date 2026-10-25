package slack

import (
	"testing"
)

func TestAllowedSlackResponseURL(t *testing.T) {
	hosts := []string{"hooks.slack.com"}
	if !allowedSlackResponseURL("https://hooks.slack.com/actions/T/B/x", hosts) {
		t.Fatal("expected allow")
	}
	if allowedSlackResponseURL("http://hooks.slack.com/actions/T/B/x", hosts) {
		t.Fatal("http must be rejected")
	}
	if allowedSlackResponseURL("https://evil.example/hooks", hosts) {
		t.Fatal("other host must be rejected")
	}
	if allowedSlackResponseURL("https://hooks.slack.com.evil/x", hosts) {
		t.Fatal("suffix host must be rejected")
	}
}
