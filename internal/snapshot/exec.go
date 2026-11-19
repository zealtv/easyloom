package snapshot

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/zealtv/easyloom/internal/proc"
)

// DefaultTimeout bounds one map --json run. A snapshot is a whole-loom
// operation. Under loom.sh v2.1.0 it cost roughly 17ms per stitch of
// subprocess churn — nearly 8s on a 446-stitch loom. v2.1.1 brought a
// 558-stitch loom under a second, but each loom runs its own deployed script,
// so this stays generous by design. Anything tighter would turn a large but
// healthy loom on an older script into a failure.
const DefaultTimeout = 2 * time.Minute

// Runner executes `map --json` against one loom.
//
// Script is the loom's address: the deployed script inside the .loom it
// operates on. It is invoked by that absolute path, never resolved from $PATH,
// because each deployment is the protocol version for its own loom.
type Runner struct {
	// Script is the absolute path to the deployed loom script.
	Script string
	// Dir is the working directory for the command. map --json works from any
	// cwd; setting it to the loom root is tidiness, not a requirement.
	Dir string
	// Timeout bounds one run. Zero means DefaultTimeout.
	Timeout time.Duration
}

// ExecError reports that map --json produced nothing parseable. It carries the
// exit status and stderr, so a refusal — the v1 "unknown command" message in
// particular — reaches the version probe intact.
type ExecError struct {
	Script string
	// Stderr is the command's stderr, trimmed.
	Stderr string
	// ExitCode is the process exit status, or -1 if it never ran or was
	// killed by a signal.
	ExitCode int
	Err      error
}

func (e *ExecError) Error() string {
	msg := fmt.Sprintf("%s map --json: %v", e.Script, e.Err)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func (e *ExecError) Unwrap() error { return e.Err }

// Run executes `map --json` and decodes the projection.
//
// A non-zero exit is not a parse failure. Broken dependencies, cycles,
// duplicate IDs and stale queue records all make map exit non-zero while still
// emitting a complete, valid projection — the same health rules as `status`.
// So stdout is parsed first, and the exit status only matters when parsing
// also failed. Getting that backwards makes every loom with one broken
// dependency look like a crash.
//
// The returned error may be a *DriftError alongside a usable snapshot, which
// means the schema moved rather than that the data is unusable. A nil snapshot
// is the only real failure, and it always carries an *ExecError.
func (r Runner) Run(ctx context.Context) (*Snapshot, error) {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.Script, "map", "--json")
	cmd.Dir = r.Dir
	cmd.WaitDelay = proc.WaitDelay
	// Cancellation must take the loom's own subprocesses with it, not just the
	// script that spawned them.
	proc.KillGroup(cmd)

	// Only stdout is JSON. Buffering stderr separately keeps diagnostic chatter
	// out of the document, and preserves it for the error.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	snap, decodeErr := Decode(stdout.Bytes())
	if snap != nil {
		// Parsed, so the projection is authoritative whatever the exit status
		// said. decodeErr here is at most drift.
		return snap, decodeErr
	}

	// Nothing usable on stdout: now the exit status is the story.
	err := runErr
	if err == nil {
		err = decodeErr
	}
	return nil, &ExecError{
		Script:   r.Script,
		Stderr:   strings.TrimSpace(stderr.String()),
		ExitCode: proc.ExitCode(runErr),
		Err:      err,
	}
}
