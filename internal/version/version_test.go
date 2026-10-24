package version

import "testing"

func TestString(t *testing.T) {
	Version, Commit = "1.2.3", "abc123"
	t.Cleanup(func() { Version, Commit = "dev", "none" })

	if got, want := String("haetae-agent"), "haetae-agent 1.2.3 (abc123)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
