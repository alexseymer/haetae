package deploy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const canaryEnv = "HAETAE_CANARY_SECRET"
const canaryVal = "should-not-leak-into-child"

// TestHelperProcess is the child used by TestExecRunnerMinimalEnv.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, canaryEnv+"=") {
			os.Stderr.WriteString("CANARY_PRESENT\n")
			os.Exit(2)
		}
	}
	os.Stdout.WriteString("OK\n")
	os.Exit(0)
}

func TestExecRunnerMinimalEnv(t *testing.T) {
	t.Setenv(canaryEnv, canaryVal)
	state := t.TempDir()
	r := ExecRunner{StateDir: state}

	var stdout strings.Builder
	err := r.Run(context.Background(), Cmd{
		Name: os.Args[0],
		Args: []string{"-test.run=TestHelperProcess", "--"},
		Env:  []string{"GO_WANT_HELPER_PROCESS=1"},
		Stdout: func(line string) {
			stdout.WriteString(line)
			stdout.WriteByte('\n')
		},
	})
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("child saw canary or failed: exit=%d out=%q err=%v", ee.ExitCode(), stdout.String(), err)
		}
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "OK") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	home := filepath.Join(state, "home")
	fi, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o777 != 0o700 {
		t.Fatalf("home mode = %o, want 0700", fi.Mode().Perm())
	}
}
