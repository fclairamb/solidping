//go:build windows

package plugins

import "os/exec"

// setProcessGroup is a no-op on Windows: a timeout kills the plugin process
// itself (exec.CommandContext's default), not its children.
func setProcessGroup(_ *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}

	return cmd.Process.Kill() //nolint:wrapcheck // best-effort kill
}
