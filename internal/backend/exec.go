package backend

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/restoredrill/restoredrill/internal/redact"
)

// allowedCommands is the read-only allowlist per backend. Adapters may only
// build these commands; a unit test asserts this for every code path.
var allowedCommands = map[string][]string{
	"restic": {"version", "snapshots", "ls", "stats", "restore"},
	"borg":   {"--version", "list", "info", "extract"},
}

// CheckAllowed verifies that args form an allowed read-only command for the
// backend kind. It is called before every child process is started.
func CheckAllowed(kind string, args []string) error {
	allowed, ok := allowedCommands[kind]
	if !ok {
		return fmt.Errorf("unknown backend kind %q", kind)
	}
	// Find the first non-flag argument: that is the subcommand. Borg's
	// version probe is the flag-shaped "--version", handled explicitly.
	sub := ""
	if kind == "borg" && len(args) > 0 && args[0] == "--version" {
		sub = "--version"
	} else {
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			sub = a
			break
		}
	}
	for _, a := range allowed {
		if sub == a {
			return nil
		}
	}
	return fmt.Errorf("backend %s: command %q is not in the read-only allowlist %v — this is a bug, please report it", kind, sub, allowed)
}

// CmdRunner executes backend binaries safely:
//   - every command is checked against the read-only allowlist,
//   - children run in their own process group; context cancellation kills
//     the whole group,
//   - stderr is captured, redacted and forwarded to Log,
//   - stdout is streamed line by line to the caller.
type CmdRunner struct {
	Kind     string // "restic" | "borg"
	Bin      string
	Dir      string   // working directory (Borg extract restores relative to it)
	Env      []string // extra environment (KEY=VALUE)
	Redactor *redact.Redactor
	Log      func(line string)
}

// Run executes bin with args. stdoutLine is called for each stdout line
// (without the newline). It returns the combined, redacted stderr and the
// process exit code.
func (r *CmdRunner) Run(ctx context.Context, args []string, stdoutLine func(string)) (stderr string, exitCode int, err error) {
	if err := CheckAllowed(r.Kind, args); err != nil {
		return "", -1, err
	}
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	cmd.Dir = r.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the whole process group on cancellation, not just the leader.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(cmd.Environ(), r.Env...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", -1, err
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return "", -1, fmt.Errorf("cannot start %s: %w — install it or set binaries.%s in restoredrill.yaml", r.Bin, err, r.Kind)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			if stdoutLine != nil {
				stdoutLine(sc.Text())
			}
		}
	}()

	// Drain stdout fully before calling Wait. Wait closes the StdoutPipe, so
	// waiting first can close the pipe before the scanner has read it, losing
	// output (a race that shows up under load as empty/truncated JSON). The
	// scanner reaches EOF when the process exits; on context cancellation the
	// whole process group is killed, which also closes stdout.
	wg.Wait()
	waitErr := cmd.Wait()

	code := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	redacted := r.Redactor.String(stderrBuf.String())
	if r.Log != nil {
		for _, line := range strings.Split(strings.TrimRight(redacted, "\n"), "\n") {
			if line != "" {
				r.Log(line)
			}
		}
	}
	return redacted, code, waitErr
}

// RunCapture executes a command and returns the full stdout (for JSON
// documents that are a single array/object rather than JSON lines).
func (r *CmdRunner) RunCapture(ctx context.Context, args []string) (stdout []byte, stderr string, code int, err error) {
	var buf bytes.Buffer
	stderr, code, err = r.Run(ctx, args, func(line string) {
		buf.WriteString(line)
		buf.WriteByte('\n')
	})
	return buf.Bytes(), stderr, code, err
}

// RunJSONLines executes a command and parses each stdout line as JSON via
// handle. Non-JSON lines are ignored (tolerant parsing), but handle may
// return an error for missing required fields.
func (r *CmdRunner) RunJSONLines(ctx context.Context, args []string, handle func(line []byte) error) (string, int, error) {
	var parseErr error
	stderr, code, err := r.Run(ctx, args, func(line string) {
		if parseErr != nil {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" || (line[0] != '{' && line[0] != '[') {
			return
		}
		if e := handle([]byte(line)); e != nil {
			parseErr = e
		}
	})
	if parseErr != nil {
		return stderr, code, parseErr
	}
	return stderr, code, err
}