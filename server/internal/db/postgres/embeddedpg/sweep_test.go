package embeddedpg

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// deadPID returns a PID that is (with overwhelming likelihood) not
// currently running, for fabricating a "dead owner" marker without booting
// or killing any real process. PID 1 is a poor choice cross-platform (it's
// often un-signalable rather than absent), so this picks a very large PID
// unlikely to be assigned on any test runner and confirms it's dead before
// handing it back.
func deadPID(t *testing.T) int {
	t.Helper()

	const candidate = 999999

	require.False(t, pidAlive(candidate), "expected PID %d to be free for this test", candidate)

	return candidate
}

// writeMarkerDir creates a directory named name under t.TempDir() (so it is
// NOT under the real os.TempDir(), keeping this test from ever touching
// real orphaned instances on the machine) with an owner.json marker for the
// given PID.
func writeMarkerDir(t *testing.T, root, name string, ownerPID int) string {
	t.Helper()

	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	marker := ownerMarker{OwnerPID: ownerPID, StartedAt: time.Now().UTC(), Suite: "test"}
	data, err := json.Marshal(marker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownerMarkerFile), data, 0o600))

	return dir
}

func TestSweepOrphans_RemovesDeadOwnerDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dead := deadPID(t)
	dir := writeMarkerDir(t, root, unifiedPrefix+"-teststale-abc123", dead)

	sweepOrphans(root)

	_, statErr := os.Stat(dir)
	require.True(t, os.IsNotExist(statErr), "expected stale dir to be removed, stat err: %v", statErr)
}

func TestSweepOrphans_LeavesLiveOwnerDirAlone(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// Our own PID is unambiguously alive for the duration of this test.
	dir := writeMarkerDir(t, root, unifiedPrefix+"-testlive-abc123", os.Getpid())

	sweepOrphans(root)

	_, statErr := os.Stat(dir)
	require.NoError(t, statErr, "expected live-owner dir to be left untouched")
}

func TestSweepOrphans_LegacyDirWithoutMarker_OldEnoughIsRemoved(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "solidping-scenario-pg-1234567")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	// Backdate mtime past legacyStaleAge so the dir is treated as abandoned.
	old := time.Now().Add(-2 * legacyStaleAge)
	require.NoError(t, os.Chtimes(dir, old, old))

	sweepOrphans(root)

	_, statErr := os.Stat(dir)
	require.True(t, os.IsNotExist(statErr), "expected old legacy dir to be removed, stat err: %v", statErr)
}

func TestSweepOrphans_LegacyDirWithoutMarker_RecentIsLeftAlone(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "postgres-test-1234567")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	// Freshly created: mtime is "now", well within legacyStaleAge.

	sweepOrphans(root)

	_, statErr := os.Stat(dir)
	require.NoError(t, statErr, "expected fresh legacy dir to be left untouched")
}

func TestSweepOrphans_UnrelatedDirIsIgnored(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "some-other-unrelated-dir")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	sweepOrphans(root)

	_, statErr := os.Stat(dir)
	require.NoError(t, statErr, "sweep must never touch directories outside its known prefixes")
}

// TestSweepOrphans_ConcurrentSuitesDoNotSweepEachOther proves two "suites"
// with distinct, both-alive owner PIDs coexist: sweeping candidates for one
// suite must not remove the data dir of another concurrently running suite.
// Uses two real long-lived subprocesses (not just our own PID twice) so the
// liveness check is exercised against genuinely different PIDs.
func TestSweepOrphans_ConcurrentSuitesDoNotSweepEachOther(t *testing.T) {
	t.Parallel()

	sleepA := exec.CommandContext(t.Context(), "sleep", "30")
	require.NoError(t, sleepA.Start())

	t.Cleanup(func() { _ = sleepA.Process.Kill() })

	sleepB := exec.CommandContext(t.Context(), "sleep", "30")
	require.NoError(t, sleepB.Start())

	t.Cleanup(func() { _ = sleepB.Process.Kill() })

	root := t.TempDir()
	dirA := writeMarkerDir(t, root, unifiedPrefix+"-suiteA-"+strconv.Itoa(sleepA.Process.Pid), sleepA.Process.Pid)
	dirB := writeMarkerDir(t, root, unifiedPrefix+"-suiteB-"+strconv.Itoa(sleepB.Process.Pid), sleepB.Process.Pid)

	sweepOrphans(root)

	_, errA := os.Stat(dirA)
	require.NoError(t, errA, "suite A's dir must survive while its owner is alive")

	_, errB := os.Stat(dirB)
	require.NoError(t, errB, "suite B's dir must survive while its owner is alive")
}

func TestIsCandidateDir(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		unifiedPrefix + "-scenario-123":        true,
		"solidping-scenario-pg-123":            true,
		"pg-notifier-test-123":                 true,
		"pg-notifier-factory-test-123":         true,
		"realtime-multireplica-123":            true,
		"postgres-test-123":                    true,
		"jobsvc-reap-pg-123":                   true,
		"some-other-dir":                       false,
		"solidping-embedded-pg-scenario-extra": true,
	}

	for name, want := range cases {
		require.Equal(t, want, isCandidateDir(name), "isCandidateDir(%q)", name)
	}
}

// A live instance has no owner marker while initdb runs (embedded-postgres
// wipes the data path first). A concurrent package's startup sweep must not
// mistake that window for an abandoned Start().
func TestSweepOrphans_UnifiedDirWithoutMarker_RecentIsLeftAlone(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, unifiedPrefix+"-initdb-in-progress-123")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	sweepOrphans(root)

	_, statErr := os.Stat(dir)
	require.NoError(t, statErr, "a markerless dir inside the init window belongs to a live Start()")
}

// Positive control for the test above: past the grace window, a markerless
// unified dir is an interrupted Start() and is still cleaned up.
func TestSweepOrphans_UnifiedDirWithoutMarker_OldIsRemoved(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, unifiedPrefix+"-interrupted-123")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	old := time.Now().Add(-2 * unmarkedGraceAge)
	require.NoError(t, os.Chtimes(dir, old, old))

	sweepOrphans(root)

	_, statErr := os.Stat(dir)
	require.True(t, os.IsNotExist(statErr), "expected the abandoned dir to be removed, stat err: %v", statErr)
}

func TestEmbeddedDataDirFromCommandLine(t *testing.T) {
	t.Parallel()

	tmp := "/var/folders/nh/x/T"

	cases := []struct {
		name    string
		cmdLine string
		want    string
	}{
		{
			name:    "postgres server",
			cmdLine: "/home/u/.embedded-postgres-go/bin/postgres -D " + tmp + "/solidping-embedded-pg-test-42 -p 15434",
			want:    tmp + "/solidping-embedded-pg-test-42",
		},
		{
			name: "initdb, whose pwfile lives in the runtime dir",
			cmdLine: "/x/bin/initdb -A password -U postgres -D " + tmp + "/solidping-embedded-pg-notifier-7 --pwfile=" +
				tmp + "/solidping-pgruntime-notifier-9/pwfile",
			want: tmp + "/solidping-embedded-pg-notifier-7",
		},
		{
			name:    "legacy scenario prefix",
			cmdLine: "/x/bin/postgres -D /tmp/solidping-scenario-pg-1234",
			want:    "/tmp/solidping-scenario-pg-1234",
		},
		{
			name:    "a file inside the data dir resolves to the data dir",
			cmdLine: "postgres --config_file=/tmp/solidping-embedded-pg-test-1/postgresql.conf",
			want:    "/tmp/solidping-embedded-pg-test-1",
		},
		{
			name:    "no embedded data dir at all",
			cmdLine: "/usr/lib/postgresql/16/bin/postgres -D /var/lib/postgresql/data",
			want:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, embeddedDataDirFromCommandLine(tc.cmdLine))
		})
	}
}
