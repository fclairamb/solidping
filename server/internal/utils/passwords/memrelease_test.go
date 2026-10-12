package passwords

import (
	"os"
	"os/exec"
	"runtime/metrics"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	memReleaseChildEnv = "SP_TEST_ARGON2_MEMRELEASE_CHILD"
	mib                = 1 << 20
)

// heapSnapshot is what the runtime holds from the OS (minus what it already
// returned) and the current GC goal.
type heapSnapshot struct {
	retained uint64
	goal     uint64
}

func readHeapSnapshot() heapSnapshot {
	samples := []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
		{Name: "/gc/heap/goal:bytes"},
	}
	metrics.Read(samples)

	return heapSnapshot{
		retained: samples[0].Value.Uint64() - samples[1].Value.Uint64(),
		goal:     samples[2].Value.Uint64(),
	}
}

// TestArgon2MemoryIsReturned: after one default-profile (64 MiB) argon2id hash
// and the release delay, the 64 MiB block is handed back to the OS and the GC
// goal is no longer inflated, without the caller forcing a GC
// (spec 2026-10-12-04). Runs in a child process so other tests' heaps do not
// interfere.
//
//nolint:paralleltest // re-execs the test binary; the child measures process-wide memory
func TestArgon2MemoryIsReturned(t *testing.T) {
	if os.Getenv(memReleaseChildEnv) == "1" {
		argon2MemoryChild(t)
		return
	}

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestArgon2MemoryIsReturned$", "-test.v")
	cmd.Env = append(os.Environ(), memReleaseChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	t.Logf("child output:\n%s", out)
	require.NoError(t, err)
}

func argon2MemoryChild(t *testing.T) {
	t.Helper()

	base := readHeapSnapshot()

	_, err := hashArgon2id("password", defaultArgon2Params)
	require.NoError(t, err)

	after := readHeapSnapshot()

	time.Sleep(releaseDelay + time.Second)

	released := readHeapSnapshot()

	t.Logf("baseline retained=%d MiB goal=%d MiB", base.retained/mib, base.goal/mib)
	t.Logf("after hash retained=%d MiB goal=%d MiB", after.retained/mib, after.goal/mib)
	t.Logf("after release retained=%d MiB goal=%d MiB", released.retained/mib, released.goal/mib)

	require.LessOrEqual(t, released.retained, base.retained+16*mib, "argon2 block must be returned to the OS")
	require.LessOrEqual(t, released.goal, uint64(48*mib), "GC goal must not stay inflated by the argon2 block")
}
