//go:build windows

package embeddedpg

import (
	"errors"
	"syscall"
)

// errKillUnsupported is returned by killPID on Windows. embeddedpg's Start
// path is unreachable there in practice — main.go refuses to start the
// postgres-embedded DB type on Windows (spec 2026-09-15-02) — so these are
// honest stubs that let the package cross-compile rather than a silent
// no-op standing in for a real port.
var errKillUnsupported = errors.New("embeddedpg: killing a process by PID is not supported on windows")

func killPID(_ int) error {
	return errKillUnsupported
}

// pidAlive always reports false on Windows: there is no signal-0 liveness
// check available via syscall here, and this package's callers never reach
// this path on Windows (see errKillUnsupported).
func pidAlive(_ int) bool {
	return false
}

// detachedSysProcAttr returns a zero-value SysProcAttr. A real detached
// session on Windows would use CREATE_NEW_PROCESS_GROUP, but startWatchdog
// already refuses to spawn the watchdog on Windows (runtime.GOOS check), so
// this value is never actually used to start a process.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
