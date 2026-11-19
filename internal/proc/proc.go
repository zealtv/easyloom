// Package proc holds the process handling every loom invocation shares.
//
// EasyLoom runs the deployed loom.sh for two different reasons — to read a
// projection and to mutate — and both need the same behaviour: the script
// addressed by absolute path, cancellation that reaches the whole process
// group, and an exit status the caller can report.
package proc

import (
	"errors"
	"os/exec"
	"time"
)

// WaitDelay bounds how long a run keeps waiting after cancellation. A loom
// spawns a subprocess per stitch, and those children inherit the stdout pipe,
// so without this the wait outlives the kill by however long the slowest
// grandchild takes. Cancelling has to actually return.
const WaitDelay = time.Second

// ExitCode reports the process exit status: 0 when the command succeeded, and
// -1 when it never ran or died on a signal.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// Started reports whether the command actually ran. A command that never
// started — a missing or unexecutable script — failed for a reason no retry
// with different arguments will fix.
func Started(err error) bool {
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}
