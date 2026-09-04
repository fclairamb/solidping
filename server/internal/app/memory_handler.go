package app

import (
	"encoding/json"
	"math"
	"net/http"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/fclairamb/solidping/server/internal/buildinfo"
	"github.com/fclairamb/solidping/server/internal/meminfo"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// Sample modes reported in MemorySample.Mode.
const (
	// sampleModeSteady is the default: read the process as it is, mid-GC-cycle
	// and all. This is what a monitoring system sees.
	sampleModeSteady = "steady"
	// sampleModeFloor is ?gc=1: force a GC and return free pages to the OS
	// before sampling, so the numbers approximate the live floor rather than
	// the steady state. The two are different measurements and must never be
	// compared with each other.
	sampleModeFloor = "floor"
)

// MemoryRuntime is the runtime.MemStats / goroutine slice of the snapshot.
type MemoryRuntime struct {
	HeapAllocBytes  uint64  `json:"heapAllocBytes"`
	HeapInuseBytes  uint64  `json:"heapInuseBytes"`
	HeapObjects     uint64  `json:"heapObjects"`
	StackInuseBytes uint64  `json:"stackInuseBytes"`
	SysBytes        uint64  `json:"sysBytes"`
	NumGoroutine    int     `json:"numGoroutine"`
	NumGC           uint32  `json:"numGc"`
	GCPauseTotalNs  uint64  `json:"gcPauseTotalNs"`
	NextGCBytes     uint64  `json:"nextGcBytes"`
	GCCPUFraction   float64 `json:"gcCpuFraction"`
	// GoMemLimitBytes is the effective GOMEMLIMIT soft cap; 0 means unlimited.
	// Lets an operator confirm the runtime guardrail took effect without scraping.
	GoMemLimitBytes int64 `json:"goMemLimitBytes"`
	// GoMaxProcs is GOMAXPROCS — relevant because per-P caches scale memory with it.
	GoMaxProcs int `json:"goMaxProcs"`
	// Classes is the runtime/metrics `/memory/classes/*` breakdown, which
	// accounts for every byte the runtime took from the OS (MemStats does not).
	// It is the Go side of the off-heap subtraction.
	Classes meminfo.RuntimeClasses `json:"classes"`
}

// MemoryProcess holds OS-level process memory. rssBytes stays what it always
// was — process_resident_memory_bytes, i.e. anon + file + shmem with no split —
// and the /proc-derived fields beside it are what make that number
// interpretable: only rssAnonBytes can cause an OOM kill, rssFileBytes is
// reclaimable page cache of the binary's own mappings.
//
// Every field but rssBytes is Linux-only; on macOS or any kernel without
// smaps_rollup the corresponding section reports present=false and zeroes,
// never an error.
type MemoryProcess struct {
	RSSBytes uint64 `json:"rssBytes"`

	// Status is /proc/self/status: the RSS anon/file/shmem split, the VmHWM
	// high-water mark, and the thread count (each OS thread is an anonymous
	// stack the Go heap profiler does not show).
	Status meminfo.ProcStatus `json:"status"`
	// Smaps is /proc/self/smaps_rollup. sharedCleanBytes is dominated by the
	// binary's mapped text/rodata: a binary-size change moves it and leaves the
	// heap alone, which is precisely why it is reported apart from the heap
	// numbers rather than added to them.
	Smaps meminfo.SmapsRollup `json:"smaps"`
}

// MemorySubsystems mirrors the A2 gauges so a scripted snapshot doesn't need to
// scrape /metrics for them.
type MemorySubsystems struct {
	DEKCacheEntries  int `json:"dekCacheEntries"`
	RateLimitEntries int `json:"rateLimitEntries"`
	EventListeners   int `json:"eventListeners"`
}

// MemoryBuild answers the off-heap question at a glance: with cgo on, the mattn
// SQLite driver allocates off-heap (invisible to pprof); with cgo off, modernc
// is on-heap.
type MemoryBuild struct {
	CGOEnabled   bool   `json:"cgoEnabled"`
	SQLiteDriver string `json:"sqliteDriver"`
	GoVersion    string `json:"goVersion"`
}

// MemoryDerived carries the numbers nobody computes by hand — and gets wrong
// when they try.
type MemoryDerived struct {
	// OffHeapBytes is RssAnon − (runtime total − heap released): resident
	// anonymous memory the Go runtime does not account for. With cgo on, this
	// is where the mattn SQLite arena shows up. Signed: a small negative value
	// means the runtime holds address space that is not resident, which is
	// information, not an error.
	OffHeapBytes int64 `json:"offHeapBytes"`
	// OffHeapKnown is false when RssAnon was unavailable (no /proc), in which
	// case OffHeapBytes is meaningless rather than zero.
	OffHeapKnown bool `json:"offHeapKnown"`
	// GoResidentBytes is (runtime total − heap released), the subtrahend above,
	// exposed so the arithmetic can be checked without recomputing it.
	GoResidentBytes uint64 `json:"goResidentBytes"`
}

// MemorySample records how this particular reading was taken, so two numbers
// are never silently compared across different measurement modes.
type MemorySample struct {
	// Mode is "steady" or "floor" (see sampleModeSteady / sampleModeFloor).
	Mode string `json:"mode"`
	// GCForced reports whether runtime.GC() + debug.FreeOSMemory() ran first.
	GCForced bool `json:"gcForced"`
	// TakenAt is the sampling instant, so a series of samples can be plotted
	// without the client having to timestamp them.
	TakenAt time.Time `json:"takenAt"`
}

// MemorySnapshot is the full payload of GET /api/mgmt/memory.
type MemorySnapshot struct {
	Runtime    MemoryRuntime    `json:"runtime"`
	Process    MemoryProcess    `json:"process"`
	Cgroup     meminfo.Cgroup   `json:"cgroup"`
	Derived    MemoryDerived    `json:"derived"`
	Subsystems MemorySubsystems `json:"subsystems"`
	Build      MemoryBuild      `json:"build"`
	Sample     MemorySample     `json:"sample"`
}

// memorySnapshotResponse wraps the snapshot in the repo-standard {data} envelope.
type memorySnapshotResponse struct {
	Data MemorySnapshot `json:"data"`
}

// processRSSBytes reads process_resident_memory_bytes from the given gatherer
// (the process collector registered in A1). Returns 0 when the series is absent
// — the process collector emits nothing on unsupported OSes, which is exactly
// the "RSS unknown" case the off-heap accounting must tolerate.
func processRSSBytes(gatherer prometheus.Gatherer) uint64 {
	families, err := gatherer.Gather()
	if err != nil {
		return 0
	}
	for _, fam := range families {
		if fam.GetName() != "process_resident_memory_bytes" {
			continue
		}
		metrics := fam.GetMetric()
		if len(metrics) == 0 {
			continue
		}
		return gaugeUint(metrics[0])
	}
	return 0
}

func gaugeUint(m *dto.Metric) uint64 {
	g := m.GetGauge()
	if g == nil {
		return 0
	}
	v := g.GetValue()
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// buildMemorySnapshot assembles the snapshot from the runtime, the process
// collector, /proc, the cgroup and the live subsystems. Exposed (lowercase,
// package-internal) so the handler test can build it without an HTTP
// round-trip.
func (s *Server) buildMemorySnapshot(gatherer prometheus.Gatherer) MemorySnapshot {
	return s.buildMemorySnapshotMode(gatherer, false)
}

// buildMemorySnapshotMode is buildMemorySnapshot with the floor-mode switch.
// When floor is true it forces a GC and hands free pages back to the OS before
// reading anything, so the result approximates the *live floor* rather than the
// steady state. Both are legitimate measurements; conflating them is how a "10 %
// reduction" turns out to be a GC phase, so the mode travels in the payload.
func (s *Server) buildMemorySnapshotMode(gatherer prometheus.Gatherer, floor bool) MemorySnapshot {
	if floor {
		runtime.GC()
		debug.FreeOSMemory()
	}

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	// debug.SetMemoryLimit(-1) reads the effective GOMEMLIMIT without changing it;
	// math.MaxInt64 is the runtime's "no limit" sentinel, normalized to 0 here.
	memLimit := debug.SetMemoryLimit(-1)
	if memLimit == math.MaxInt64 {
		memLimit = 0
	}

	osMem := meminfo.Collect(meminfo.DefaultRoots())

	mode := sampleModeSteady
	if floor {
		mode = sampleModeFloor
	}

	snap := MemorySnapshot{
		Sample: MemorySample{Mode: mode, GCForced: floor, TakenAt: time.Now().UTC()},
		Runtime: MemoryRuntime{
			HeapAllocBytes:  memStats.HeapAlloc,
			HeapInuseBytes:  memStats.HeapInuse,
			HeapObjects:     memStats.HeapObjects,
			StackInuseBytes: memStats.StackInuse,
			SysBytes:        memStats.Sys,
			NumGoroutine:    runtime.NumGoroutine(),
			NumGC:           memStats.NumGC,
			GCPauseTotalNs:  memStats.PauseTotalNs,
			NextGCBytes:     memStats.NextGC,
			GCCPUFraction:   memStats.GCCPUFraction,
			GoMemLimitBytes: memLimit,
			GoMaxProcs:      runtime.GOMAXPROCS(0),
			Classes:         osMem.Classes,
		},
		Process: MemoryProcess{
			RSSBytes: processRSSBytes(gatherer),
			Status:   osMem.Status,
			Smaps:    osMem.Smaps,
		},
		Cgroup: osMem.Cgroup,
		Derived: MemoryDerived{
			OffHeapBytes:    osMem.OffHeapBytes,
			OffHeapKnown:    osMem.OffHeapKnown,
			GoResidentBytes: osMem.Classes.TotalBytes - osMem.Classes.HeapReleasedBytes,
		},
		Build: MemoryBuild{
			CGOEnabled:   buildinfo.CGOEnabled,
			SQLiteDriver: buildinfo.SQLiteDriver(),
			GoVersion:    buildinfo.GoVersion(),
		},
	}

	if s.services != nil {
		if cred := s.services.Credentials; cred != nil {
			snap.Subsystems.DEKCacheEntries = cred.DEKCacheLen()
		}
		if ev := s.services.EventNotifier; ev != nil {
			snap.Subsystems.EventListeners = notifier.ListenerCount(ev)
		}
	}
	if s.rateLimiter != nil {
		snap.Subsystems.RateLimitEntries = s.rateLimiter.EntryCount()
	}

	return snap
}

// getMemory handles GET /api/mgmt/memory. Super-admin gated (memstats and
// subsystem cardinality are operationally sensitive). Returns a JSON snapshot
// for humans and scripts; the raw pprof surface stays on the localhost-bound
// profiler server.
//
// `?gc=1` switches to floor mode (see buildMemorySnapshotMode). It is opt-in
// because a forced GC plus FreeOSMemory is a real, visible pause — fine for a
// measurement run, wrong for a monitoring scrape.
func (s *Server) getMemory(writer http.ResponseWriter, req *http.Request) error {
	snap := s.buildMemorySnapshotMode(prometheus.DefaultGatherer, wantsFloorMode(req))

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	data, err := json.Marshal(memorySnapshotResponse{Data: snap})
	if err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

// wantsFloorMode reports whether the request asked for the GC'd floor reading.
// Accepts the usual truthy spellings so a curl by hand and a script agree.
func wantsFloorMode(req *http.Request) bool {
	if req == nil {
		return false
	}
	switch req.URL.Query().Get("gc") {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
