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

// unmarkedGraceAge is how long a unified-prefix directory may exist without
// its owner.json marker before the sweep treats it as abandoned.
//
// A live instance has no marker for a while: embedded-postgres wipes the whole
// data path before initdb (cleanDataDirectoryAndInit), taking the marker Start
// wrote with it, and Start only writes it again once the server is up. With
// `go test -p 1` nothing else could be sweeping during that window; with
// packages running concurrently, another package's startup sweep would see a
// markerless dir, kill the initdb or postgres inside it and delete it. That is
// the "signal: killed" initdb failure the Postgres layer hit at -p 4.
const unmarkedGraceAge = 10 * time.Minute

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
		// Unified-prefix dir with an unreadable/missing marker: abandoned once
		// it is older than the init window (see unmarkedGraceAge). A healthy
		// Start() rewrites the marker as soon as the server is up, so a
		// markerless dir past that age means Start() itself was interrupted.
		if time.Since(info.ModTime()) < unmarkedGraceAge {
			return
		}
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

// pidAlive and killPID are platform-specific — see proc_unix.go / proc_windows.go.

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
	cmdLine, ok := processCommandLine(pid)

	return ok && strings.Contains(cmdLine, needle)
}

// processCommandLine returns the running process pid's command line.
func processCommandLine(pid int) (string, bool) {
	cmd := exec.CommandContext(context.Background(), "ps", "-o", "command=", "-p", strconv.Itoa(pid))

	out, err := cmd.Output()
	if err != nil {
		return "", false
	}

	return strings.TrimSpace(string(out)), true
}

// embeddedDataDirFromCommandLine extracts the embedded-postgres data
// directory a postgres/initdb command line refers to (its -D argument, in
// practice), or "" when none of its arguments names one.
func embeddedDataDirFromCommandLine(cmdLine string) string {
	for _, field := range strings.Fields(cmdLine) {
		slash := strings.Index(field, "/")
		if slash < 0 {
			continue
		}

		// The argument may name the data dir itself (-D <dir>) or a file
		// inside it (--config=<dir>/postgresql.conf): walk up to the
		// component that is the data dir.
		for path := filepath.Clean(field[slash:]); path != "/" && path != "."; path = filepath.Dir(path) {
			if isCandidateDir(filepath.Base(path)) {
				return path
			}
		}
	}

	return ""
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

	_ = killPID(pid)
}

// sweepFallbackPgrep catches postgres processes whose data directory was
// already removed (e.g. a previous sweep pass removed the dir but the
// process itself survived, or the dir was manually deleted) by matching on
// the process command line directly via pgrep, then re-verifying via ps
// before killing — same PID-reuse guard as killPostgresForDataDir.
//
// A process whose data directory still exists is NOT this function's
// business: the directory pass above already decided, with the owner-liveness
// check, whether it is an orphan. Killing it here regardless is what made
// concurrently running test packages (and two suites on one laptop) kill each
// other's live instances.
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
		cmdLine, ok := processCommandLine(pid)
		ours := strings.Contains(cmdLine, "solidping-embedded-pg") || strings.Contains(cmdLine, "solidping-scenario-pg")
		if !ok || !ours {
			continue
		}

		if dataDir := embeddedDataDirFromCommandLine(cmdLine); dataDir != "" {
			if _, statErr := os.Stat(dataDir); statErr == nil {
				continue // still has its data dir: the directory pass owns that decision
			}
		}

		fmt.Fprintf(os.Stderr, sweepLogPrefix+"killing orphaned postgres pid %d found via pgrep fallback\n", pid)
		_ = killPID(pid)
	}
}
