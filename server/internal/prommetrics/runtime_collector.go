package prommetrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// registerRuntimeCollectors adds the Go runtime and process collectors to reg.
// These yield the headline memory-analysis time series that are otherwise
// missing: go_memstats_heap_inuse_bytes, go_memstats_heap_alloc_bytes,
// go_memstats_stack_inuse_bytes, go_goroutines, go_gc_duration_seconds,
// go_memstats_next_gc_bytes, and process_resident_memory_bytes /
// process_virtual_memory_bytes.
//
// The RSS-vs-heap-inuse pair is the leak signal and the off-heap detector: a
// growing process_resident_memory_bytes while go_memstats_heap_inuse_bytes is
// flat points at off-heap (cgo/SQLite-mattn) or fragmentation rather than a Go
// leak. Registered on every role's registry (API server and worker) so both
// processes expose the same surface on /metrics.
func registerRuntimeCollectors(reg prometheus.Registerer) {
	reg.MustRegister(collectors.NewGoCollector(
		collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll),
	))
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}
