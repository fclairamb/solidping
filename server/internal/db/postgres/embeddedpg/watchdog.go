package embeddedpg

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// watchdogScript polls the owner PID every 5s; when the owner is gone for
// any reason (including SIGKILL, which bypasses defer/t.Cleanup in the
// owner), it terminates postgres (TERM, then KILL after a grace period) and
// removes the data directory, then exits. On a normal Stop(), the owner
// removes the data dir and stops postgres itself first, so these actions
// become no-ops (missing PID / missing dir) by the time they'd run — Stop()
// also kills this script's PID directly so it doesn't linger for the ~5s
// poll interval.
const watchdogScript = `
while kill -0 "$1" 2>/dev/null; do
  sleep 5
done
kill "$2" 2>/dev/null
sleep 2
kill -9 "$2" 2>/dev/null
rm -rf "$3"
`

// watchdogHandle lets Stop() kill the watchdog process directly instead of
// waiting for it to notice the data dir is gone.
type watchdogHandle struct {
	pid int
}

// kill best-effort terminates the watchdog process. Safe to call even if
// the watchdog already exited.
func (w *watchdogHandle) kill() {
	if w == nil || w.pid <= 0 {
		return
	}

	_ = syscall.Kill(w.pid, syscall.SIGKILL)
}

// startWatchdog spawns a detached process that outlives ownerPID's SIGKILL
// (own session via Setsid, stdio detached) and reaps the postgres instance
// rooted at dataDir if ownerPID disappears. Returns nil (no watchdog) on
// unsupported platforms — Windows is not a target; the startup sweep is the
// safety net there. Best-effort: a failure to spawn is logged and does not
// fail Start().
func startWatchdog(ownerPID int, dataDir string) *watchdogHandle {
	if runtime.GOOS == "windows" {
		return nil
	}

	pgPID, err := readPostmasterPID(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, sweepLogPrefix+"watchdog: could not read postmaster.pid, skipping: %v\n", err)

		return nil
	}

	cmd := exec.Command("sh", "-c", watchdogScript, "watchdog",
		fmt.Sprintf("%d", ownerPID), fmt.Sprintf("%d", pgPID), dataDir)

	// Detach stdio so nothing ties the watchdog's pipes back to the parent.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	// New session: not part of the owner's process group, so it survives a
	// SIGKILL sent to the owner's group as well as to the owner alone.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if startErr := cmd.Start(); startErr != nil {
		fmt.Fprintf(os.Stderr, sweepLogPrefix+"watchdog: failed to start: %v\n", startErr)

		return nil
	}

	// Reap the watchdog's own exit asynchronously so it never becomes a
	// zombie of this process; we don't care about its exit status.
	go func() { _ = cmd.Wait() }()

	return &watchdogHandle{pid: cmd.Process.Pid}
}
