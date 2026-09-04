package prommetrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/fclairamb/solidping/server/internal/meminfo"
)

// memInfoCollector exposes the Linux memory breakdown (/proc/self/status,
// smaps_rollup, cgroup v2) that neither the Go collector nor the process
// collector provides. Read at scrape time, so it costs nothing when nobody
// scrapes.
//
// Why these and not just process_resident_memory_bytes: that series is
// anon + file + shmem in one number, and only the anon part can get the process
// OOM-killed. Splitting them is the difference between "RSS grew 40 MB, panic"
// and "40 MB of the binary's own rodata got paged in and will be dropped again".
//
// Every series here is namespaced solidping_*, so none of these descriptors can
// collide with the Go/process collectors registered alongside them.
type memInfoCollector struct {
	collect func() meminfo.Snapshot

	rssAnon      *prometheus.Desc
	rssFile      *prometheus.Desc
	rssShmem     *prometheus.Desc
	vmHWM        *prometheus.Desc
	threads      *prometheus.Desc
	pss          *prometheus.Desc
	privateDirty *prometheus.Desc
	privateClean *prometheus.Desc
	sharedClean  *prometheus.Desc

	cgroupCurrent       *prometheus.Desc
	cgroupPeak          *prometheus.Desc
	cgroupMax           *prometheus.Desc
	cgroupAnon          *prometheus.Desc
	cgroupFile          *prometheus.Desc
	cgroupKernel        *prometheus.Desc
	cgroupUnreclaimable *prometheus.Desc

	offHeap *prometheus.Desc
}

// NewMemInfoCollector returns a collector reading the live process. The
// snapshot source is injectable so a test can drive it from fixtures.
func NewMemInfoCollector() prometheus.Collector {
	return newMemInfoCollector(func() meminfo.Snapshot {
		return meminfo.Collect(meminfo.DefaultRoots())
	})
}

func newMemInfoCollector(collect func() meminfo.Snapshot) *memInfoCollector {
	desc := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(name, help, nil, nil)
	}

	return &memInfoCollector{
		collect: collect,
		rssAnon: desc("solidping_process_rss_anon_bytes",
			"Resident anonymous memory (/proc/self/status RssAnon) — the only RSS component an OOM kill can be caused by"),
		rssFile: desc("solidping_process_rss_file_bytes",
			"Resident file-backed memory (RssFile) — mapped binary/rodata pages, reclaimable"),
		rssShmem: desc("solidping_process_rss_shmem_bytes",
			"Resident shared-memory pages (RssShmem)"),
		vmHWM: desc("solidping_process_rss_peak_bytes",
			"Peak resident set size since start (/proc/self/status VmHWM)"),
		threads: desc("solidping_process_threads",
			"OS threads in the process (/proc/self/status Threads); each costs an anonymous stack"),
		pss: desc("solidping_process_smaps_pss_bytes",
			"Proportional set size (/proc/self/smaps_rollup Pss)"),
		privateDirty: desc("solidping_process_smaps_private_dirty_bytes",
			"Private dirty pages (smaps_rollup Private_Dirty)"),
		privateClean: desc("solidping_process_smaps_private_clean_bytes",
			"Private clean pages (smaps_rollup Private_Clean)"),
		sharedClean: desc("solidping_process_smaps_shared_clean_bytes",
			"Shared clean pages (smaps_rollup Shared_Clean) — dominated by the binary's mapped text/rodata"),

		cgroupCurrent: desc("solidping_cgroup_memory_current_bytes",
			"cgroup memory.current — total charged to this container"),
		cgroupPeak: desc("solidping_cgroup_memory_peak_bytes",
			"cgroup memory.peak — high-water mark of memory.current"),
		cgroupMax: desc("solidping_cgroup_memory_max_bytes",
			"cgroup memory.max — the hard limit the OOM killer enforces (absent when unlimited)"),
		cgroupAnon: desc("solidping_cgroup_memory_anon_bytes",
			"cgroup memory.stat anon"),
		cgroupFile: desc("solidping_cgroup_memory_file_bytes",
			"cgroup memory.stat file — reclaimable page cache"),
		cgroupKernel: desc("solidping_cgroup_memory_kernel_bytes",
			"cgroup memory.stat kernel (slab, sockets, page tables, stacks)"),
		cgroupUnreclaimable: desc("solidping_cgroup_memory_unreclaimable_bytes",
			"cgroup anon + kernel — what the kernel cannot reclaim, and therefore what an OOM kill is decided on"),

		offHeap: desc("solidping_process_offheap_bytes",
			"RssAnon minus the Go runtime's resident total — cgo/foreign anonymous memory invisible to pprof"),
	}
}

// Describe implements prometheus.Collector.
func (c *memInfoCollector) Describe(out chan<- *prometheus.Desc) {
	all := []*prometheus.Desc{
		c.rssAnon, c.rssFile, c.rssShmem, c.vmHWM, c.threads,
		c.pss, c.privateDirty, c.privateClean, c.sharedClean,
		c.cgroupCurrent, c.cgroupPeak, c.cgroupMax, c.cgroupAnon, c.cgroupFile,
		c.cgroupKernel, c.cgroupUnreclaimable, c.offHeap,
	}
	for _, desc := range all {
		out <- desc
	}
}

// Collect implements prometheus.Collector. Absent sources emit **no series** at
// all rather than zeros: a zero RssAnon on a Mac would read as "this process
// uses no anonymous memory", which is worse than no data.
func (c *memInfoCollector) Collect(out chan<- prometheus.Metric) {
	snap := c.collect()

	gauge := func(desc *prometheus.Desc, value float64) {
		out <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value)
	}

	if snap.Status.Present {
		gauge(c.rssAnon, float64(snap.Status.RssAnonBytes))
		gauge(c.rssFile, float64(snap.Status.RssFileBytes))
		gauge(c.rssShmem, float64(snap.Status.RssShmemBytes))
		gauge(c.vmHWM, float64(snap.Status.VMHWMBytes))
		gauge(c.threads, float64(snap.Status.Threads))
	}

	if snap.Smaps.Present {
		gauge(c.pss, float64(snap.Smaps.PssBytes))
		gauge(c.privateDirty, float64(snap.Smaps.PrivateDirtyBytes))
		gauge(c.privateClean, float64(snap.Smaps.PrivateCleanBytes))
		gauge(c.sharedClean, float64(snap.Smaps.SharedCleanBytes))
	}

	if snap.Cgroup.Present {
		gauge(c.cgroupCurrent, float64(snap.Cgroup.CurrentBytes))
		gauge(c.cgroupAnon, float64(snap.Cgroup.AnonBytes))
		gauge(c.cgroupFile, float64(snap.Cgroup.FileBytes))
		gauge(c.cgroupKernel, float64(snap.Cgroup.KernelBytes))
		gauge(c.cgroupUnreclaimable, float64(snap.Cgroup.UnreclaimableBytes))
		if snap.Cgroup.PeakBytes > 0 {
			gauge(c.cgroupPeak, float64(snap.Cgroup.PeakBytes))
		}
		// 0 means "unlimited" here, and an unlimited cgroup has no limit series
		// to report — emitting 0 would make every alert on it fire.
		if snap.Cgroup.MaxBytes > 0 {
			gauge(c.cgroupMax, float64(snap.Cgroup.MaxBytes))
		}
	}

	if snap.OffHeapKnown {
		gauge(c.offHeap, float64(snap.OffHeapBytes))
	}
}
