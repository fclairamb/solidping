package app

import (
	"encoding/json"
	"math"
	"net/http"
	"runtime"
	"runtime/debug"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/buildinfo"
	"github.com/fclairamb/solidping/server/internal/notifier"
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
}

// MemoryProcess holds OS-level process memory. rssBytes makes the
// off-heap gap (RSS − Go heap) visible in the same payload.
type MemoryProcess struct {
	RSSBytes uint64 `json:"rssBytes"`
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

// MemorySnapshot is the full payload of GET /api/mgmt/memory.
type MemorySnapshot struct {
	Runtime    MemoryRuntime    `json:"runtime"`
	Process    MemoryProcess    `json:"process"`
	Subsystems MemorySubsystems `json:"subsystems"`
	Build      MemoryBuild      `json:"build"`
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
// collector, and the live subsystems. Exposed (lowercase, package-internal) so
// the handler test can build it without an HTTP round-trip.
func (s *Server) buildMemorySnapshot(gatherer prometheus.Gatherer) MemorySnapshot {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	// debug.SetMemoryLimit(-1) reads the effective GOMEMLIMIT without changing it;
	// math.MaxInt64 is the runtime's "no limit" sentinel, normalized to 0 here.
	memLimit := debug.SetMemoryLimit(-1)
	if memLimit == math.MaxInt64 {
		memLimit = 0
	}

	snap := MemorySnapshot{
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
		},
		Process: MemoryProcess{
			RSSBytes: processRSSBytes(gatherer),
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
func (s *Server) getMemory(writer http.ResponseWriter, _ bunrouter.Request) error {
	snap := s.buildMemorySnapshot(prometheus.DefaultGatherer)

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	data, err := json.Marshal(memorySnapshotResponse{Data: snap})
	if err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}
