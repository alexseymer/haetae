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
	// userinfo must not bypass Hostname() exact match.
	if allowedSlackResponseURL("https://hooks.slack.com@evil.example/x", hosts) {
		t.Fatal("userinfo host trick must be rejected")
	}
	if allowedSlackResponseURL("https://user:pass@evil.example/x", hosts) {
		t.Fatal("userinfo on evil host must be rejected")
	}
	// Default https port is stripped by Hostname(); still exact host match.
	if !allowedSlackResponseURL("https://hooks.slack.com:443/actions/T/B/x", hosts) {
		t.Fatal(":443 on exact host must be allowed")
	}
}
