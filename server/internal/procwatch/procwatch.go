// Package procwatch supervises this process's relationship with the process
// that started it.
//
// It exists for one failure mode: a server started as a child of a test harness
// (or any ad-hoc wrapper) that dies without cleaning up. On POSIX the child is
// not killed with its parent — it is reparented to init and keeps running, so
// one crashed harness run leaves a server holding a port and hammering a
// database nobody is watching. That is exactly how a solidping server survived
// its session by seventeen hours (spec 2026-08-12-05).
//
// The check is a poll rather than a signal: Linux's PR_SET_PDEATHSIG has no
// portable equivalent (macOS has none at all), and one getppid() per second
// costs nothing.
package procwatch

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// DefaultInterval is how often the parent is checked when none is given.
const DefaultInterval = time.Second

// initPID is the pid a reparented orphan is handed to on POSIX.
const initPID = 1

// ParentWatcher calls a callback once the process that started this one is
// gone.
type ParentWatcher struct {
	// Interval between checks. Zero uses DefaultInterval.
	Interval time.Duration
	// PPID reads the current parent pid. Zero uses os.Getppid.
	PPID func() int
	// Logger receives the one line emitted when the parent disappears. Zero
	// uses slog.Default().
	Logger *slog.Logger
}

// Run blocks until ctx is done or the parent process disappears, in which case
// it logs one line and calls onOrphaned exactly once before returning.
//
// "Disappeared" means the parent pid changed from the one observed at start —
// which on POSIX means it became 1 (init/launchd adopted us). A process already
// started with pid 1 as its parent (a container entrypoint, a daemonized
// server) has nothing to watch, so Run returns immediately.
func (w ParentWatcher) Run(ctx context.Context, onOrphaned func()) {
	getPPID := w.PPID
	if getPPID == nil {
		getPPID = os.Getppid
	}

	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}

	interval := w.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	original := getPPID()
	if original == initPID {
		logger.InfoContext(ctx,
			"Parent-death watch disabled: this process has no parent to outlive")

		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if current := getPPID(); current != original {
				logger.WarnContext(ctx,
					"Parent process is gone, shutting down rather than being orphaned",
					"original_ppid", original, "current_ppid", current)

				if onOrphaned != nil {
					onOrphaned()
				}

				return
			}
		}
	}
}
