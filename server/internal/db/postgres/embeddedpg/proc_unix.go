//go:build !windows

package embeddedpg

import "syscall"

// killPID best-effort sends SIGKILL to pid. Errors are ignored by every
// caller — the process may already be gone, which is the desired end state
// either way.
func killPID(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// pidAlive reports whether a process with the given PID currently exists.
// syscall.Kill with signal 0 sends no signal but still validates the PID —
// the standard liveness-check idiom on Unix.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	return syscall.Kill(pid, 0) == nil
}

// detachedSysProcAttr starts the child in its own session (Setsid), so it
// survives a SIGKILL sent to the parent's process group as well as to the
// parent alone.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
