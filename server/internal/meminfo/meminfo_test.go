package meminfo

import (
	"os"
	"path/filepath"
	"testing"
)

// procStatusFixture is a trimmed but structurally faithful /proc/self/status
// from a Linux container (tabs after the colon, values in kB).
const procStatusFixture = `Name:	solidping
Umask:	0022
State:	S (sleeping)
Tgid:	1
Pid:	1
VmPeak:	  2543216 kB
VmSize:	  2412144 kB
VmHWM:	   215044 kB
VmRSS:	   198320 kB
RssAnon:	   150016 kB
RssFile:	    48304 kB
RssShmem:	        0 kB
Threads:	38
voluntary_ctxt_switches:	1234
`

const smapsRollupFixture = `00400000-7ffd0b3ff000 ---p 00000000 00:00 0                          [rollup]
Rss:              198320 kB
Pss:              182004 kB
Pss_Dirty:        150016 kB
Shared_Clean:      16300 kB
Shared_Dirty:          0 kB
Private_Clean:     32004 kB
Private_Dirty:    150016 kB
Referenced:       198320 kB
Anonymous:        150016 kB
`

// cgroupV2StatFixture keeps the key order the kernel uses and includes keys we
// deliberately ignore, so the parser is proven to pick by name.
const cgroupV2StatFixture = `anon 157286400
file 50331648
kernel 8388608
kernel_stack 1179648
pagetables 2097152
percpu 65536
sock 131072
vmalloc 0
shmem 0
file_mapped 47185920
file_dirty 0
slab_reclaimable 3145728
slab_unreclaimable 2097152
slab 5242880
workingset_refault_anon 0
`

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestParseProcStatus(t *testing.T) {
	t.Parallel()

	got := ParseProcStatus([]byte(procStatusFixture))

	if !got.Present {
		t.Fatal("expected Present")
	}
	checks := []struct {
		name string
		got  uint64
		want uint64
	}{
		{"RssAnon", got.RssAnonBytes, 150016 * 1024},
		{"RssFile", got.RssFileBytes, 48304 * 1024},
		{"RssShmem", got.RssShmemBytes, 0},
		{"VmHWM", got.VMHWMBytes, 215044 * 1024},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if got.Threads != 38 {
		t.Errorf("Threads = %d, want 38", got.Threads)
	}
}

func TestParseProcStatusGarbage(t *testing.T) {
	t.Parallel()

	// A truncated read mid-line, and a value that is not a number: neither may
	// panic, and neither may poison the fields that did parse.
	got := ParseProcStatus([]byte("RssAnon:\t   4096 kB\nRssFile:\tnot-a-number kB\nThreads:\tzz\nVmHW"))

	if got.RssAnonBytes != 4096*1024 {
		t.Errorf("RssAnon = %d, want %d", got.RssAnonBytes, 4096*1024)
	}
	if got.RssFileBytes != 0 {
		t.Errorf("RssFile = %d, want 0 for an unparseable value", got.RssFileBytes)
	}
	if got.Threads != 0 {
		t.Errorf("Threads = %d, want 0 for an unparseable value", got.Threads)
	}
}

func TestParseSmapsRollup(t *testing.T) {
	t.Parallel()

	got := ParseSmapsRollup([]byte(smapsRollupFixture))

	if !got.Present {
		t.Fatal("expected Present")
	}
	if got.PssBytes != 182004*1024 {
		t.Errorf("Pss = %d, want %d", got.PssBytes, 182004*1024)
	}
	if got.PrivateDirtyBytes != 150016*1024 {
		t.Errorf("Private_Dirty = %d", got.PrivateDirtyBytes)
	}
	if got.PrivateCleanBytes != 32004*1024 {
		t.Errorf("Private_Clean = %d", got.PrivateCleanBytes)
	}
	if got.SharedCleanBytes != 16300*1024 {
		t.Errorf("Shared_Clean = %d", got.SharedCleanBytes)
	}
}

func TestParseCgroupValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in      string
		want    uint64
		wantOK  bool
		comment string
	}{
		{"1073741824\n", 1073741824, true, "plain limit"},
		{"max\n", 0, true, "v2 unlimited sentinel is present-but-unlimited"},
		{"9223372036854771712\n", 0, true, "v1 near-MaxInt64 sentinel"},
		{"", 0, false, "empty file"},
		{"garbage", 0, false, "unparseable"},
	}
	for _, c := range cases {
		got, ok := ParseCgroupValue(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("ParseCgroupValue(%q) = (%d,%v), want (%d,%v) — %s", c.in, got, ok, c.want, c.wantOK, c.comment)
		}
	}
}

func TestReadCgroupV2(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	v2 := filepath.Join(dir, "cgroup")
	writeFile(t, filepath.Join(v2, "memory.current"), "220200960\n")
	writeFile(t, filepath.Join(v2, "memory.peak"), "251658240\n")
	writeFile(t, filepath.Join(v2, "memory.max"), "1073741824\n")
	writeFile(t, filepath.Join(v2, "memory.stat"), cgroupV2StatFixture)

	got := ReadCgroup(Roots{CgroupV2: v2, CgroupV1: filepath.Join(dir, "absent")})

	if !got.Present || got.Version != 2 {
		t.Fatalf("Present=%v Version=%d, want true/2", got.Present, got.Version)
	}
	if got.CurrentBytes != 220200960 || got.PeakBytes != 251658240 || got.MaxBytes != 1073741824 {
		t.Errorf("current/peak/max = %d/%d/%d", got.CurrentBytes, got.PeakBytes, got.MaxBytes)
	}
	if got.AnonBytes != 157286400 || got.KernelBytes != 8388608 {
		t.Errorf("anon/kernel = %d/%d", got.AnonBytes, got.KernelBytes)
	}
	if got.FileMappedBytes != 47185920 || got.SlabBytes != 5242880 || got.SockBytes != 131072 {
		t.Errorf("file_mapped/slab/sock = %d/%d/%d", got.FileMappedBytes, got.SlabBytes, got.SockBytes)
	}
	// The primary metric of the bench harness: anon + kernel.
	if want := uint64(157286400 + 8388608); got.UnreclaimableBytes != want {
		t.Errorf("Unreclaimable = %d, want %d", got.UnreclaimableBytes, want)
	}
}

// TestReadCgroupV2NoKernelKey pins the pre-6.0 fallback: without a `kernel`
// roll-up the unreclaimable total must still account for kernel memory rather
// than silently reporting anon only.
func TestReadCgroupV2NoKernelKey(t *testing.T) {
	t.Parallel()

	v2 := filepath.Join(t.TempDir(), "cgroup")
	writeFile(t, filepath.Join(v2, "memory.current"), "100\n")
	writeFile(t, filepath.Join(v2, "memory.stat"),
		"anon 1000\nfile 500\nslab 300\nsock 100\npercpu 50\nkernel_stack 20\npagetables 30\n")

	got := ReadCgroup(Roots{CgroupV2: v2})

	if got.KernelBytes != 500 {
		t.Errorf("kernel fallback = %d, want 500 (slab+sock+percpu+kernel_stack+pagetables)", got.KernelBytes)
	}
	if got.UnreclaimableBytes != 1500 {
		t.Errorf("Unreclaimable = %d, want 1500", got.UnreclaimableBytes)
	}
}

func TestReadCgroupV1(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	v1 := filepath.Join(dir, "memory")
	writeFile(t, filepath.Join(v1, "memory.usage_in_bytes"), "220200960\n")
	writeFile(t, filepath.Join(v1, "memory.max_usage_in_bytes"), "251658240\n")
	writeFile(t, filepath.Join(v1, "memory.limit_in_bytes"), "9223372036854771712\n")
	writeFile(t, filepath.Join(v1, "memory.stat"), "cache 50331648\nrss 157286400\nmapped_file 47185920\nshmem 0\n")

	got := ReadCgroup(Roots{CgroupV2: filepath.Join(dir, "absent"), CgroupV1: v1})

	if !got.Present || got.Version != 1 {
		t.Fatalf("Present=%v Version=%d, want true/1", got.Present, got.Version)
	}
	if got.AnonBytes != 157286400 || got.FileBytes != 50331648 {
		t.Errorf("anon/file = %d/%d", got.AnonBytes, got.FileBytes)
	}
	if got.MaxBytes != 0 {
		t.Errorf("MaxBytes = %d, want 0 for the v1 unlimited sentinel", got.MaxBytes)
	}
}

// TestCollectAbsentTree is the macOS / no-cgroup case: everything missing must
// yield a usable zero snapshot, never an error and never a panic.
func TestCollectAbsentTree(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	got := Collect(Roots{
		Proc:     filepath.Join(dir, "nope"),
		CgroupV2: filepath.Join(dir, "nope"),
		CgroupV1: filepath.Join(dir, "nope"),
	})

	if got.Status.Present || got.Smaps.Present || got.Cgroup.Present {
		t.Errorf("expected everything absent, got %+v", got)
	}
	if got.OffHeapKnown {
		t.Error("off-heap must be unknown without /proc, not a fake zero")
	}
	// runtime/metrics is always available, so classes must still be populated:
	// that is what makes the snapshot useful on a dev Mac.
	if got.Classes.TotalBytes == 0 {
		t.Error("runtime classes should be populated even with no /proc")
	}
}

func TestCollectLinuxTree(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	proc := filepath.Join(dir, "proc")
	writeFile(t, filepath.Join(proc, "status"), procStatusFixture)
	writeFile(t, filepath.Join(proc, "smaps_rollup"), smapsRollupFixture)
	v2 := filepath.Join(dir, "cgroup")
	writeFile(t, filepath.Join(v2, "memory.current"), "220200960\n")
	writeFile(t, filepath.Join(v2, "memory.stat"), cgroupV2StatFixture)

	got := Collect(Roots{Proc: proc, CgroupV2: v2, CgroupV1: filepath.Join(dir, "absent")})

	if !got.Status.Present || !got.Smaps.Present || !got.Cgroup.Present {
		t.Fatalf("expected a fully populated snapshot, got %+v", got)
	}
	if !got.OffHeapKnown {
		t.Fatal("off-heap should be derivable when RssAnon is present")
	}
	// RssAnon here (≈150 MB) dwarfs this test binary's Go runtime total, so the
	// off-heap gap must come out large and positive — the sign convention is
	// what the runbook's rule depends on.
	if got.OffHeapBytes <= 0 {
		t.Errorf("OffHeapBytes = %d, want > 0 for a 150 MB RssAnon fixture", got.OffHeapBytes)
	}
}

func TestReadRuntimeClasses(t *testing.T) {
	t.Parallel()

	got := ReadRuntimeClasses()

	if got.TotalBytes == 0 {
		t.Fatal("total classes must be non-zero in a live process")
	}
	// total is the sum of every class, so any single class must be ≤ total —
	// a cheap invariant that catches a mistyped metric name.
	if got.HeapObjectsBytes > got.TotalBytes {
		t.Errorf("heap objects %d > total %d", got.HeapObjectsBytes, got.TotalBytes)
	}
	if got.HeapLiveBytes > got.TotalBytes {
		t.Errorf("heap live %d > total %d", got.HeapLiveBytes, got.TotalBytes)
	}
}
