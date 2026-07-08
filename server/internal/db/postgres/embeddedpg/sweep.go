package embeddedpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// errPostmasterPIDEmpty is returned when postmaster.pid exists but contains
// no lines to parse a PID from.
var errPostmasterPIDEmpty = errors.New("postmaster.pid is empty")

// legacyPrefixes are directory-name prefixes used by call sites before they
// were migrated onto this package's unified naming scheme. The sweep
// recognizes them for one transition period so pre-existing orphans (and any
// leftovers from a partially-migrated deploy) still get cleaned up.
var legacyPrefixes = []string{ //nolint:gochecknoglobals // package-level constant list
	"solidping-scenario-pg-",
	"pg-notifier-test-",
	"pg-notifier-factory-test-",
	"realtime-multireplica-",
	"postgres-test-",
	"jobsvc-reap-pg-",
}

// legacyStaleAge is how old (by mtime) a legacy directory without an
// owner.json marker must be before the sweep considers it abandoned. Legacy
// dirs predate the marker, so liveness can't be checked directly — age is
// the fallback signal.
const legacyStaleAge = 1 * time.Hour

// sweepLogPrefix tags every best-effort diagnostic the sweep logs to
// stderr, so it's easy to grep for and easy to tell apart from actual test
// output.
const sweepLogPrefix = "embeddedpg: "

// isCandidateDir reports whether name (a directory basename under
// os.TempDir()) looks like an embedded-postgres data directory this package
// is responsible for sweeping.
func isCandidateDir(name string) bool {
	if strings.HasPrefix(name, unifiedPrefix+"-") {
		return true
	}

	for _, prefix := range legacyPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return false
}

// isLegacyDir reports whether name matches one of the pre-migration
// prefixes (as opposed to the unified prefix, which always carries an
// owner.json marker).
func isLegacyDir(name string) bool {
	for _, prefix := range legacyPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return false
}

// sweepOrphans scans root for stale embedded-postgres data directories left
// behind by previous runs (typically a test process that was killed hard
// enough to skip its own cleanup) and removes them. It is best-effort: any
// error sweeping one candidate is logged to stderr and the sweep continues.
// It must never fail the caller's test run.
func sweepOrphans(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, sweepLogPrefix+"failed to read %s: %v\n", root, err)

		return
	}

	for _, entry := range entries {
		if !entry.IsDir() || !isCandidateDir(entry.Name()) {
			continue
		}

		dir := filepath.Join(root, entry.Name())
		sweepCandidate(dir, entry.Name())
	}

	sweepFallbackPgrep()
}

// sweepCandidate decides whether a single candidate directory is an orphan
// and, if so, kills its postgres process (if still running) and removes it.
func sweepCandidate(dir, baseName string) {
	info, statErr := os.Stat(dir)
	if statErr != nil {
		// Already gone (raced with another sweep or manual cleanup) — nothing to do.
		return
	}

	marker, markerErr := readOwnerMarker(dir)

	switch {
	case markerErr == nil:
		if pidAlive(marker.OwnerPID) {
			// A concurrently running suite owns this dir — leave it alone.
			return
		}
	case isLegacyDir(baseName):
		// Legacy dir predates the marker: fall back to mtime-based staleness.
		if time.Since(info.ModTime()) < legacyStaleAge {
			return
		}
	default:
		// Unified-prefix dir with an unreadable/missing marker: treat as
		// abandoned (a healthy Start() always writes the marker before
		// returning, so its absence means Start() itself was interrupted).
	}

	killPostgresForDataDir(dir)

	if rmErr := os.RemoveAll(dir); rmErr != nil {
		fmt.Fprintf(os.Stderr, sweepLogPrefix+"failed to remove stale dir %s: %v\n", dir, rmErr)
	} else {
		fmt.Fprintf(os.Stderr, sweepLogPrefix+"removed orphaned data dir %s\n", dir)
	}
}

// readOwnerMarker reads and parses the owner.json marker from dataDir.
func readOwnerMarker(dataDir string) (ownerMarker, error) {
	var marker ownerMarker

	data, err := os.ReadFile(filepath.Join(dataDir, ownerMarkerFile))
	if err != nil {
		return marker, fmt.Errorf("failed to read owner marker: %w", err)
	}

	if unmarshalErr := json.Unmarshal(data, &marker); unmarshalErr != nil {
		return marker, fmt.Errorf("failed to parse owner marker: %w", unmarshalErr)
	}

	return marker, nil
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

// readPostmasterPID reads the postgres PID from the first line of
// postmaster.pid in dataDir, as written by postgres itself.
func readPostmasterPID(dataDir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "postmaster.pid"))
	if err != nil {
		return 0, fmt.Errorf("failed to read postmaster.pid: %w", err)
	}

	lines := strings.SplitN(string(data), "\n", 2)
	if len(lines) == 0 {
		return 0, errPostmasterPIDEmpty
	}

	pid, parseErr := strconv.Atoi(strings.TrimSpace(lines[0]))
	if parseErr != nil {
		return 0, fmt.Errorf("failed to parse postmaster.pid: %w", parseErr)
	}

	return pid, nil
}

// commandLineContains reports whether the running process pid's command
// line contains needle. Used to guard against PID reuse before killing a
// process found via postmaster.pid or pgrep: a stale PID could have been
// recycled by an unrelated process since the data dir was abandoned.
func commandLineContains(pid int, needle string) bool {
	cmd := exec.CommandContext(context.Background(), "ps", "-o", "command=", "-p", strconv.Itoa(pid))

	out, err := cmd.Output()
	if err != nil {
		return false
	}

	return strings.Contains(string(out), needle)
}

// killPostgresForDataDir kills the postgres process that owns dataDir, if
// one is still running, guarding against PID reuse by checking the
// process's command line references this exact data dir before sending a
// signal.
func killPostgresForDataDir(dataDir string) {
	pid, err := readPostmasterPID(dataDir)
	if err != nil {
		// No postmaster.pid (postgres never started, or already reaped) —
		// nothing to kill.
		return
	}

	if !pidAlive(pid) {
		return
	}

	if !commandLineContains(pid, dataDir) {
		// PID was reused by an unrelated process since this data dir was
		// abandoned — do not touch it.
		return
	}

	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// sweepFallbackPgrep catches postgres processes whose data directory was
// already removed (e.g. a previous sweep pass removed the dir but the
// process itself survived, or the dir was manually deleted) by matching on
// the process command line directly via pgrep, then re-verifying via ps
// before killing — same PID-reuse guard as killPostgresForDataDir.
func sweepFallbackPgrep() {
	cmd := exec.CommandContext(context.Background(), "pgrep", "-f", "solidping-embedded-pg|solidping-scenario-pg")

	out, err := cmd.Output()
	if err != nil {
		// pgrep exits non-zero when there are no matches — not an error worth logging.
		return
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}

		pid, parseErr := strconv.Atoi(strings.TrimSpace(line))
		if parseErr != nil {
			continue
		}

		if pid == os.Getpid() {
			continue // pgrep can match our own process's arguments; never self-kill.
		}

		// Re-verify the command line actually references one of our
		// prefixes (pgrep already filtered on this pattern, but re-checking
		// here keeps the safety invariant local to one function and
		// protects against a future pattern change becoming too broad).
		if !commandLineContains(pid, "solidping-embedded-pg") && !commandLineContains(pid, "solidping-scenario-pg") {
			continue
		}

		fmt.Fprintf(os.Stderr, sweepLogPrefix+"killing orphaned postgres pid %d found via pgrep fallback\n", pid)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
