// Package meminfo turns the Linux memory pseudo-files and the Go runtime's own
// accounting into one structured snapshot, so "what does this process cost in
// memory?" has a single, precise answer instead of three vaguely-related
// numbers.
//
// Three different "RSS"es are routinely conflated:
//
//   - `kubectl top` reports the cgroup **working set** (`memory.current` minus
//     inactive file pages), which includes actively mapped file-backed pages of
//     the binary itself;
//   - `process_resident_memory_bytes` (`/proc/self/stat`) is anon + file +
//     shmem, with no split;
//   - the Go runtime sees neither a cgo allocator's arena, nor OS thread
//     stacks, nor the binary's text/rodata.
//
// The OOM killer uses yet a fourth number: the cgroup's unreclaimable memory
// (`anon + kernel`). That is why [Cgroup.UnreclaimableBytes] is computed here
// once, server-side, rather than left to whoever reads the JSON.
//
// Every reader in this package is total: an absent file (macOS, cgroup v1, no
// cgroup at all) yields a zero value with Present=false and never an error. The
// parsers are pure functions over bytes so they can be fixture-tested from any
// OS.
package meminfo

import (
	"os"
	"path/filepath"
	"runtime/metrics"
	"strconv"
	"strings"
)

// kilobyte is the unit /proc/self/status and smaps_rollup report in ("kB").
const kilobyte = 1024

// cgroupUnlimited is the sentinel cgroup v2 writes in memory.max, and the
// floor above which a cgroup v1 limit means "no limit" (v1 reports a value near
// math.MaxInt64 rounded down to the page size).
const cgroupUnlimitedFloor = uint64(1) << 62

// Roots locates the pseudo-filesystems the snapshot is read from. Injected so
// tests can point at a fixture tree instead of the live kernel.
type Roots struct {
	// Proc is the per-process directory, normally /proc/self.
	Proc string
	// CgroupV2 is the unified-hierarchy mount, normally /sys/fs/cgroup. Inside
	// a container the cgroup namespace roots here, so a direct read returns the
	// container's own numbers.
	CgroupV2 string
	// CgroupV1 is the legacy memory controller directory.
	CgroupV1 string
}

// DefaultRoots returns the live-kernel locations.
func DefaultRoots() Roots {
	return Roots{
		Proc:     "/proc/self",
		CgroupV2: "/sys/fs/cgroup",
		CgroupV1: "/sys/fs/cgroup/memory",
	}
}

// ProcStatus is the useful subset of /proc/self/status: the RSS split the
// single `rssBytes` number hides, the high-water mark, and the thread count
// (each OS thread costs a stack, which is anonymous memory the Go heap
// profiler does not show).
type ProcStatus struct {
	Present       bool   `json:"present"`
	RssAnonBytes  uint64 `json:"rssAnonBytes"`
	RssFileBytes  uint64 `json:"rssFileBytes"`
	RssShmemBytes uint64 `json:"rssShmemBytes"`
	VmHWMBytes    uint64 `json:"vmHwmBytes"`
	Threads       int    `json:"threads"`
}

// SmapsRollup is the useful subset of /proc/self/smaps_rollup. SharedClean is
// dominated by the binary's mapped text/rodata, which is why a binary-size
// change moves it while leaving the heap untouched.
type SmapsRollup struct {
	Present           bool   `json:"present"`
	PssBytes          uint64 `json:"pssBytes"`
	PrivateDirtyBytes uint64 `json:"privateDirtyBytes"`
	PrivateCleanBytes uint64 `json:"privateCleanBytes"`
	SharedCleanBytes  uint64 `json:"sharedCleanBytes"`
}

// Cgroup is the container's own memory accounting — the numbers the OOM killer
// actually acts on.
type Cgroup struct {
	Present bool `json:"present"`
	// Version is 2 for the unified hierarchy, 1 for the legacy controller, 0
	// when no cgroup was found.
	Version int `json:"version"`
	// CurrentBytes is memory.current (v2) / memory.usage_in_bytes (v1).
	CurrentBytes uint64 `json:"currentBytes"`
	// PeakBytes is memory.peak (v2, kernel ≥ 5.19) / memory.max_usage_in_bytes
	// (v1). Zero when the kernel does not expose it.
	PeakBytes uint64 `json:"peakBytes"`
	// MaxBytes is the hard limit; 0 means unlimited ("max" in v2).
	MaxBytes uint64 `json:"maxBytes"`

	AnonBytes       uint64 `json:"anonBytes"`
	FileBytes       uint64 `json:"fileBytes"`
	FileMappedBytes uint64 `json:"fileMappedBytes"`
	KernelBytes     uint64 `json:"kernelBytes"`
	SlabBytes       uint64 `json:"slabBytes"`
	SockBytes       uint64 `json:"sockBytes"`
	ShmemBytes      uint64 `json:"shmemBytes"`

	// UnreclaimableBytes is anon + kernel: what the kernel cannot reclaim under
	// pressure, and therefore what an OOM kill is decided on. This is the
	// primary metric of the memory bench harness.
	UnreclaimableBytes uint64 `json:"unreclaimableBytes"`
}

// RuntimeClasses is the runtime/metrics `/memory/classes/*` breakdown plus the
// live-heap estimate. Unlike runtime.MemStats it accounts for every byte the
// runtime obtained from the OS, which is what makes the off-heap subtraction
// below meaningful.
type RuntimeClasses struct {
	TotalBytes            uint64 `json:"totalBytes"`
	HeapObjectsBytes      uint64 `json:"heapObjectsBytes"`
	HeapUnusedBytes       uint64 `json:"heapUnusedBytes"`
	HeapFreeBytes         uint64 `json:"heapFreeBytes"`
	HeapReleasedBytes     uint64 `json:"heapReleasedBytes"`
	OSStacksBytes         uint64 `json:"osStacksBytes"`
	MetadataBytes         uint64 `json:"metadataBytes"`
	OtherBytes            uint64 `json:"otherBytes"`
	ProfilingBucketsBytes uint64 `json:"profilingBucketsBytes"`
	HeapLiveBytes         uint64 `json:"heapLiveBytes"`
}

// Snapshot is the whole picture at one instant.
type Snapshot struct {
	Status  ProcStatus     `json:"status"`
	Smaps   SmapsRollup    `json:"smaps"`
	Cgroup  Cgroup         `json:"cgroup"`
	Classes RuntimeClasses `json:"classes"`

	// OffHeapBytes is RssAnon − (runtime total − heap released): the anonymous
	// memory that is resident but not accounted for by the Go runtime — a cgo
	// allocator's arena (mattn SQLite), thread stacks the runtime did not
	// create, or an mmap done by a C library. The runbook's §4 "off-heap rule"
	// asks for this number; this is where it is computed.
	//
	// Signed on purpose: a small negative value is real information (the
	// runtime holds address space that is not resident) and must not be
	// clamped away into a fake zero.
	OffHeapBytes int64 `json:"offHeapBytes"`
	// OffHeapKnown is false when RssAnon was unavailable (no /proc), in which
	// case OffHeapBytes is meaningless rather than zero.
	OffHeapKnown bool `json:"offHeapKnown"`
}

// Collect reads everything available under roots. It never fails: whatever is
// missing is simply reported as absent.
func Collect(roots Roots) Snapshot {
	snap := Snapshot{
		Status:  ReadProcStatus(roots.Proc),
		Smaps:   ReadSmapsRollup(roots.Proc),
		Cgroup:  ReadCgroup(roots),
		Classes: ReadRuntimeClasses(),
	}
	snap.OffHeapBytes, snap.OffHeapKnown = offHeap(snap.Status, snap.Classes)

	return snap
}

// offHeap derives the off-heap gap, reporting whether it could be derived at
// all.
func offHeap(status ProcStatus, classes RuntimeClasses) (int64, bool) {
	if !status.Present {
		return 0, false
	}

	// Released pages have been handed back to the OS: the runtime still counts
	// them in total, but they are no longer resident, so they must not be
	// charged against RssAnon.
	goResident := int64(classes.TotalBytes) - int64(classes.HeapReleasedBytes) //nolint:gosec // byte counts, far below int64 range

	return int64(status.RssAnonBytes) - goResident, true //nolint:gosec // byte counts, far below int64 range
}

// ReadProcStatus reads <procDir>/status. Absent or unreadable → zero value.
func ReadProcStatus(procDir string) ProcStatus {
	data, err := os.ReadFile(filepath.Join(procDir, "status"))
	if err != nil {
		return ProcStatus{}
	}

	return ParseProcStatus(data)
}

// ParseProcStatus parses the `Key:\tvalue kB` format of /proc/<pid>/status.
// Unknown keys are ignored; a malformed value leaves its field at zero rather
// than failing the whole parse.
func ParseProcStatus(data []byte) ProcStatus {
	status := ProcStatus{Present: true}

	forEachColonLine(data, func(key, value string) {
		switch key {
		case "RssAnon":
			status.RssAnonBytes = parseKB(value)
		case "RssFile":
			status.RssFileBytes = parseKB(value)
		case "RssShmem":
			status.RssShmemBytes = parseKB(value)
		case "VmHWM":
			status.VmHWMBytes = parseKB(value)
		case "Threads":
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				status.Threads = n
			}
		}
	})

	return status
}

// ReadSmapsRollup reads <procDir>/smaps_rollup. Absent (older kernels, macOS)
// → zero value.
func ReadSmapsRollup(procDir string) SmapsRollup {
	data, err := os.ReadFile(filepath.Join(procDir, "smaps_rollup"))
	if err != nil {
		return SmapsRollup{}
	}

	return ParseSmapsRollup(data)
}

// ParseSmapsRollup parses /proc/<pid>/smaps_rollup, which shares the
// `Key: value kB` shape of status after a leading address-range header line.
func ParseSmapsRollup(data []byte) SmapsRollup {
	rollup := SmapsRollup{Present: true}

	forEachColonLine(data, func(key, value string) {
		switch key {
		case "Pss":
			rollup.PssBytes = parseKB(value)
		case "Private_Dirty":
			rollup.PrivateDirtyBytes = parseKB(value)
		case "Private_Clean":
			rollup.PrivateCleanBytes = parseKB(value)
		case "Shared_Clean":
			rollup.SharedCleanBytes = parseKB(value)
		}
	})

	return rollup
}

// ReadCgroup reads the container's memory accounting, preferring the unified
// (v2) hierarchy and falling back to the legacy (v1) controller.
func ReadCgroup(roots Roots) Cgroup {
	if cg, ok := readCgroupV2(roots.CgroupV2); ok {
		return cg
	}
	if cg, ok := readCgroupV1(roots.CgroupV1); ok {
		return cg
	}

	return Cgroup{}
}

// readCgroupV2 reads the unified hierarchy. memory.current is the marker file:
// without it there is no v2 memory controller here.
func readCgroupV2(dir string) (Cgroup, bool) {
	current, ok := readCgroupValue(filepath.Join(dir, "memory.current"))
	if !ok {
		return Cgroup{}, false
	}

	cg := Cgroup{Present: true, Version: 2, CurrentBytes: current}
	if peak, ok := readCgroupValue(filepath.Join(dir, "memory.peak")); ok {
		cg.PeakBytes = peak
	}
	if maxBytes, ok := readCgroupValue(filepath.Join(dir, "memory.max")); ok {
		cg.MaxBytes = maxBytes
	}

	if data, err := os.ReadFile(filepath.Join(dir, "memory.stat")); err == nil {
		applyCgroupV2Stat(&cg, ParseKeyValueBytes(data))
	}
	cg.UnreclaimableBytes = cg.AnonBytes + cg.KernelBytes

	return cg, true
}

// applyCgroupV2Stat maps the cgroup v2 memory.stat keys onto the struct.
func applyCgroupV2Stat(cg *Cgroup, stat map[string]uint64) {
	cg.AnonBytes = stat["anon"]
	cg.FileBytes = stat["file"]
	cg.FileMappedBytes = stat["file_mapped"]
	cg.KernelBytes = stat["kernel"]
	cg.SlabBytes = stat["slab"]
	cg.SockBytes = stat["sock"]
	cg.ShmemBytes = stat["shmem"]

	// `kernel` only exists on kernels ≥ 6.0. Older kernels expose the pieces,
	// so approximate it from slab + sock + the page tables rather than
	// reporting an unreclaimable total that is silently missing kernel memory.
	if cg.KernelBytes == 0 {
		cg.KernelBytes = stat["slab"] + stat["sock"] + stat["percpu"] + stat["kernel_stack"] + stat["pagetables"]
	}
}

// readCgroupV1 reads the legacy memory controller and maps it onto the same
// shape. v1 has no `kernel` roll-up, so UnreclaimableBytes is anon-only there —
// documented rather than faked.
func readCgroupV1(dir string) (Cgroup, bool) {
	usage, ok := readCgroupValue(filepath.Join(dir, "memory.usage_in_bytes"))
	if !ok {
		return Cgroup{}, false
	}

	cg := Cgroup{Present: true, Version: 1, CurrentBytes: usage}
	if peak, ok := readCgroupValue(filepath.Join(dir, "memory.max_usage_in_bytes")); ok {
		cg.PeakBytes = peak
	}
	if limit, ok := readCgroupValue(filepath.Join(dir, "memory.limit_in_bytes")); ok {
		cg.MaxBytes = limit
	}

	if data, err := os.ReadFile(filepath.Join(dir, "memory.stat")); err == nil {
		stat := ParseKeyValueBytes(data)
		cg.AnonBytes = stat["rss"]
		cg.FileBytes = stat["cache"]
		cg.FileMappedBytes = stat["mapped_file"]
		cg.ShmemBytes = stat["shmem"]
	}
	cg.UnreclaimableBytes = cg.AnonBytes + cg.KernelBytes

	return cg, true
}

// readCgroupValue reads a single-value cgroup file, normalizing both versions'
// "unlimited" spellings to 0.
func readCgroupValue(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}

	return ParseCgroupValue(string(data))
}

// ParseCgroupValue parses a single-value cgroup file. "max" (v2) and v1's
// near-MaxInt64 sentinel both mean "no limit" and are reported as (0, true) —
// the file exists, the limit does not.
func ParseCgroupValue(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if s == "max" {
		return 0, true
	}

	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	if v >= cgroupUnlimitedFloor {
		return 0, true
	}

	return v, true
}

// ParseKeyValueBytes parses the `key value` (space-separated, one per line)
// format used by cgroup memory.stat in both versions. Unparseable lines are
// skipped.
func ParseKeyValueBytes(data []byte) map[string]uint64 {
	out := make(map[string]uint64)

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		out[fields[0]] = v
	}

	return out
}

// runtimeMetricNames are the runtime/metrics samples the classes breakdown
// needs. Kept in one slice so the read is a single metrics.Read call.
//
//nolint:gochecknoglobals // immutable metric-name table
var runtimeMetricNames = []string{
	"/memory/classes/total:bytes",
	"/memory/classes/heap/objects:bytes",
	"/memory/classes/heap/unused:bytes",
	"/memory/classes/heap/free:bytes",
	"/memory/classes/heap/released:bytes",
	"/memory/classes/os-stacks:bytes",
	"/memory/classes/metadata/mcache/free:bytes",
	"/memory/classes/metadata/mcache/inuse:bytes",
	"/memory/classes/metadata/mspan/free:bytes",
	"/memory/classes/metadata/mspan/inuse:bytes",
	"/memory/classes/metadata/other:bytes",
	"/memory/classes/other:bytes",
	"/memory/classes/profiling/buckets:bytes",
	"/gc/heap/live:bytes",
}

// ReadRuntimeClasses samples the runtime's own memory accounting. The metadata
// sub-classes are summed into one number because they are only ever read
// together.
func ReadRuntimeClasses() RuntimeClasses {
	samples := make([]metrics.Sample, len(runtimeMetricNames))
	for i, name := range runtimeMetricNames {
		samples[i].Name = name
	}
	metrics.Read(samples)

	values := make(map[string]uint64, len(samples))
	for _, s := range samples {
		if s.Value.Kind() == metrics.KindUint64 {
			values[s.Name] = s.Value.Uint64()
		}
	}

	return RuntimeClasses{
		TotalBytes:        values["/memory/classes/total:bytes"],
		HeapObjectsBytes:  values["/memory/classes/heap/objects:bytes"],
		HeapUnusedBytes:   values["/memory/classes/heap/unused:bytes"],
		HeapFreeBytes:     values["/memory/classes/heap/free:bytes"],
		HeapReleasedBytes: values["/memory/classes/heap/released:bytes"],
		OSStacksBytes:     values["/memory/classes/os-stacks:bytes"],
		MetadataBytes: values["/memory/classes/metadata/mcache/free:bytes"] +
			values["/memory/classes/metadata/mcache/inuse:bytes"] +
			values["/memory/classes/metadata/mspan/free:bytes"] +
			values["/memory/classes/metadata/mspan/inuse:bytes"] +
			values["/memory/classes/metadata/other:bytes"],
		OtherBytes:            values["/memory/classes/other:bytes"],
		ProfilingBucketsBytes: values["/memory/classes/profiling/buckets:bytes"],
		HeapLiveBytes:         values["/gc/heap/live:bytes"],
	}
}

// forEachColonLine walks `Key:<whitespace>value` lines, which is the shape of
// both /proc/<pid>/status and smaps_rollup.
func forEachColonLine(data []byte, fn func(key, value string)) {
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fn(strings.TrimSpace(key), value)
	}
}

// parseKB converts a `  1234 kB` field to bytes. A missing or malformed number
// yields 0 — the caller's field simply stays unset.
func parseKB(value string) uint64 {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0
	}

	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}

	// Values without a unit suffix are already bytes; the kernel only ever
	// writes "kB" here, but tolerating both keeps a fixture honest.
	if len(fields) > 1 && strings.EqualFold(fields[1], "kB") {
		return n * kilobyte
	}

	return n
}
