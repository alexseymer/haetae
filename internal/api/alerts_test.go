package api

import (
	"testing"

	"github.com/alexseymer/haetae/internal/store"
)

func TestValidateOrgSettings(t *testing.T) {
	if err := validateOrgSettings("UTC", 8, 90, 90, 90, 300, 120); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if err := validateOrgSettings("Not/AZone", 8, 90, 90, 90, 300, 120); err == nil {
		t.Fatal("want invalid time zone")
	}
	if err := validateOrgSettings("UTC", 24, 90, 90, 90, 300, 120); err == nil {
		t.Fatal("want briefingHour range error")
	}
	if err := validateOrgSettings("UTC", 8, 0, 90, 90, 300, 120); err == nil {
		t.Fatal("want cpuPct range error")
	}
	if err := validateOrgSettings("UTC", 8, 90, 90, 90, 5, 120); err == nil {
		t.Fatal("want sustainedS range error")
	}
	if err := validateOrgSettings("UTC", 8, 90, 90, 90, 300, 10); err == nil {
		t.Fatal("want offlineAfterS range error")
	}
}

func TestValidMuteRule(t *testing.T) {
	if !validMuteRule(store.RuleCrashLoop) {
		t.Fatal("crash_loop should be valid")
	}
	if validMuteRule("nope") {
		t.Fatal("unknown rule")
	}
}

func TestMaskExternalID(t *testing.T) {
	if got := maskExternalID("ab"); got != "ab" {
		t.Fatalf("short: %q", got)
	}
	if got := maskExternalID("abcdef"); got != "***def" {
		t.Fatalf("masked: %q", got)
	}
}
