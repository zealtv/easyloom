//go:build !unix

package proc

import "os/exec"

// KillGroup has no portable equivalent off unix, so cancellation falls back to
// the default: kill the script and rely on WaitDelay to stop waiting on
// whatever it spawned.
func KillGroup(cmd *exec.Cmd) {}
