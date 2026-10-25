package deploy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// maxLineBytes caps one line of command output (matches redact.MaxLineBytes).
const maxLineBytes = 64 << 10

// Cmd is one external process. Name and Args are passed to os/exec with no shell.
type Cmd struct {
	Dir            string
	Env            []string // appended to a minimal environment; see ExecRunner
	Name           string
	Args           []string
	Stdout, Stderr func(line string)
}

// Runner runs external commands. All git and docker invocations go through it.
type Runner interface {
	Run(ctx context.Context, cmd Cmd) error
}

// ExecRunner runs commands with os/exec: no shell, stdin closed, process-group
// kill on cancel, and line-oriented stdout/stderr with a 64 KiB line cap.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, c Cmd) error {
	if c.Name == "" {
		return errors.New("deploy: empty command name")
	}
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = nil

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	// CommandContext only kills the process, not its children. Kill the group.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanLines(stdout, c.Stdout)
	}()
	go func() {
		defer wg.Done()
		scanLines(stderr, c.Stderr)
	}()
	wg.Wait()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func scanLines(r io.Reader, emit func(string)) {
	br := bufio.NewReaderSize(r, maxLineBytes)
	for {
		line, err := readLine(br)
		if len(line) > 0 && emit != nil {
			emit(line)
		}
		if err != nil {
			return
		}
	}
}

// readLine returns one line without the trailing newline. Lines longer than
// maxLineBytes are truncated and the rest of the line is discarded.
func readLine(br *bufio.Reader) (string, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(chunk) > 0 {
			if len(buf)+len(chunk) > maxLineBytes {
				need := maxLineBytes - len(buf)
				if need > 0 {
					buf = append(buf, chunk[:need]...)
				}
				// Drain the rest of this overlong line.
				for isPrefix && err == nil {
					_, isPrefix, err = br.ReadLine()
				}
				return string(buf), err
			}
			buf = append(buf, chunk...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(buf) > 0 {
				return string(buf), nil
			}
			return string(buf), err
		}
		if !isPrefix {
			return string(buf), nil
		}
	}
}

// CmdRecord is a command as seen by FakeRunner (callbacks omitted).
type CmdRecord struct {
	Dir  string
	Env  []string
	Name string
	Args []string
}

// FakeStep scripts one FakeRunner response. When Match is nil, the step is
// consumed in order for any call. When Match is set, the step applies to the
// first matching call (Repeat < 0 means forever).
type FakeStep struct {
	Match  func(Cmd) bool
	Stdout []string
	Stderr []string
	Err    error
	Repeat int       // 0 = once, -1 = unlimited
	After  func(Cmd) // optional side effect after recording the call
}

// FakeRunner records calls and returns scripted outputs for tests.
type FakeRunner struct {
	mu    sync.Mutex
	Calls []CmdRecord
	steps []FakeStep
}

// Add appends scripted steps.
func (f *FakeRunner) Add(steps ...FakeStep) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, steps...)
}

// Run implements Runner.
func (f *FakeRunner) Run(ctx context.Context, c Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.Calls = append(f.Calls, CmdRecord{Dir: c.Dir, Env: append([]string(nil), c.Env...), Name: c.Name, Args: append([]string(nil), c.Args...)})
	step, ok := f.takeStep(c)
	f.mu.Unlock()
	if !ok {
		return fmt.Errorf("deploy: FakeRunner: unexpected command %s %v", c.Name, c.Args)
	}
	if step.After != nil {
		step.After(c)
	}
	for _, line := range step.Stdout {
		if c.Stdout != nil {
			c.Stdout(line)
		}
	}
	for _, line := range step.Stderr {
		if c.Stderr != nil {
			c.Stderr(line)
		}
	}
	if step.Err != nil {
		return step.Err
	}
	return ctx.Err()
}

func (f *FakeRunner) takeStep(c Cmd) (FakeStep, bool) {
	for i, s := range f.steps {
		if s.Match != nil && !s.Match(c) {
			continue
		}
		if s.Match == nil {
			// Sequential unmatched step: only the first with nil Match.
			out := s
			if s.Repeat < 0 {
				return out, true
			}
			f.steps = append(f.steps[:i], f.steps[i+1:]...)
			return out, true
		}
		out := s
		if s.Repeat < 0 {
			return out, true
		}
		if s.Repeat > 1 {
			f.steps[i].Repeat--
			return out, true
		}
		f.steps = append(f.steps[:i], f.steps[i+1:]...)
		return out, true
	}
	return FakeStep{}, false
}

// MatchNameArgs returns a matcher for Name and exact Args.
func MatchNameArgs(name string, args ...string) func(Cmd) bool {
	return func(c Cmd) bool {
		if c.Name != name || len(c.Args) != len(args) {
			return false
		}
		for i := range args {
			if c.Args[i] != args[i] {
				return false
			}
		}
		return true
	}
}

// MatchHasArg reports whether args contain s.
func MatchHasArg(name, s string) func(Cmd) bool {
	return func(c Cmd) bool {
		if c.Name != name {
			return false
		}
		for _, a := range c.Args {
			if a == s {
				return true
			}
		}
		return false
	}
}
