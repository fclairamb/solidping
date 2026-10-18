//go:build !windows

package plugins

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the command in its own process group, so a timeout
// kills the plugin and every child it forked.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}

	// A negative pid signals the whole group.
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) //nolint:wrapcheck // best-effort kill
}
