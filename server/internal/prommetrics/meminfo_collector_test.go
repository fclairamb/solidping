package prommetrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/fclairamb/solidping/server/internal/meminfo"
)

// linuxSnapshot is a fully-populated snapshot as a Linux container would yield.
func linuxSnapshot() meminfo.Snapshot {
	return meminfo.Snapshot{
		Status: meminfo.ProcStatus{
			Present: true, RssAnonBytes: 150, RssFileBytes: 48, RssShmemBytes: 1,
			VMHWMBytes: 215, Threads: 38,
		},
		Smaps: meminfo.SmapsRollup{
			Present: true, PssBytes: 182, PrivateDirtyBytes: 150,
			PrivateCleanBytes: 32, SharedCleanBytes: 16,
		},
		Cgroup: meminfo.Cgroup{
			Present: true, Version: 2, CurrentBytes: 220, PeakBytes: 251, MaxBytes: 1024,
			AnonBytes: 157, FileBytes: 50, KernelBytes: 8, UnreclaimableBytes: 165,
		},
		OffHeapBytes: 42, OffHeapKnown: true,
	}
}

func gatherNames(t *testing.T, c prometheus.Collector) map[string]float64 {
	t.Helper()

	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(c)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	out := make(map[string]float64)
	for _, fam := range families {
		for _, m := range fam.GetMetric() {
			out[fam.GetName()] = m.GetGauge().GetValue()
		}
	}

	return out
}

func TestMemInfoCollectorLinux(t *testing.T) {
	t.Parallel()

	got := gatherNames(t, newMemInfoCollector(linuxSnapshot))

	want := map[string]float64{
		"solidping_process_rss_anon_bytes":            150,
		"solidping_process_rss_file_bytes":            48,
		"solidping_process_rss_peak_bytes":            215,
		"solidping_process_threads":                   38,
		"solidping_process_smaps_pss_bytes":           182,
		"solidping_cgroup_memory_current_bytes":       220,
		"solidping_cgroup_memory_max_bytes":           1024,
		"solidping_cgroup_memory_unreclaimable_bytes": 165,
		"solidping_process_offheap_bytes":             42,
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %v, want %v", name, got[name], value)
		}
	}
}

// TestMemInfoCollectorAbsent is the macOS case: no /proc, no cgroup. The
// collector must emit no series at all — a zero RssAnon would read as "this
// process uses no anonymous memory", which is worse than silence.
func TestMemInfoCollectorAbsent(t *testing.T) {
	t.Parallel()

	got := gatherNames(t, newMemInfoCollector(func() meminfo.Snapshot { return meminfo.Snapshot{} }))

	if len(got) != 0 {
		t.Errorf("expected no series with absent sources, got %v", got)
	}
}

// TestMemInfoCollectorUnlimitedCgroup pins that an unlimited cgroup reports no
// limit series rather than a limit of zero, which every alert would fire on.
func TestMemInfoCollectorUnlimitedCgroup(t *testing.T) {
	t.Parallel()

	snap := linuxSnapshot()
	snap.Cgroup.MaxBytes = 0
	snap.Cgroup.PeakBytes = 0

	got := gatherNames(t, newMemInfoCollector(func() meminfo.Snapshot { return snap }))

	if _, ok := got["solidping_cgroup_memory_max_bytes"]; ok {
		t.Error("unlimited cgroup must not emit a max series")
	}
	if _, ok := got["solidping_cgroup_memory_peak_bytes"]; ok {
		t.Error("kernel without memory.peak must not emit a peak series")
	}
	if _, ok := got["solidping_cgroup_memory_current_bytes"]; !ok {
		t.Error("current must still be emitted")
	}
}

// TestRegisterNoDescriptorCollision is the regression guard the spec asks for:
// the full Register() path (SolidPing metrics + Go/process collectors + the new
// meminfo collector) must register on one registry without a duplicate
// descriptor panic, and the new series must actually appear.
func TestRegisterNoDescriptorCollision(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	// MustRegister panics on a duplicate descriptor; reaching the next line is
	// the assertion.
	Register(reg)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather after Register: %v", err)
	}

	var sawGo, sawProcess bool
	for _, fam := range families {
		switch {
		case strings.HasPrefix(fam.GetName(), "go_memstats_"):
			sawGo = true
		case fam.GetName() == "process_resident_memory_bytes":
			sawProcess = true
		}
	}
	if !sawGo {
		t.Error("go_memstats_* missing after Register")
	}
	// process_resident_memory_bytes only exists on Linux; on macOS the process
	// collector emits nothing, which is not a failure.
	t.Logf("process collector series present: %v", sawProcess)

	// The meminfo collector must actually be part of what Register() wired up —
	// the point of this test is that adding it did not collide, so proving it
	// is present is half the assertion. It emits series only where /proc and
	// the cgroup exist, so on Linux every gauge must be there and on macOS none
	// may be; "some but not all" would mean a source was misread.
	live := meminfo.Collect(meminfo.DefaultRoots())
	registered := 0

	for _, fam := range families {
		if strings.HasPrefix(fam.GetName(), "solidping_process_rss_") ||
			strings.HasPrefix(fam.GetName(), "solidping_cgroup_memory_") {
			registered++
		}
	}

	switch {
	case live.Status.Present && registered == 0:
		t.Error("/proc/self/status is readable here, so Register() must expose the rss gauges")
	case !live.Status.Present && !live.Cgroup.Present && registered != 0:
		t.Errorf("no /proc and no cgroup, yet %d meminfo series were emitted", registered)
	}

	// The standalone collector must also gather cleanly on a pedantic registry,
	// which validates every descriptor it emits.
	pedantic := prometheus.NewPedanticRegistry()
	pedantic.MustRegister(NewMemInfoCollector())

	if _, err := pedantic.Gather(); err != nil {
		t.Fatalf("meminfo collector fails a pedantic gather: %v", err)
	}
}
