//go:build unix

package proc

import (
	"os/exec"
	"syscall"
)

// KillGroup puts the run in its own process group and signals the whole group
// on cancellation. The default cancel only kills the script itself, which
// leaves the loom's per-stitch subprocesses running and holding the output
// pipe open.
func KillGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
