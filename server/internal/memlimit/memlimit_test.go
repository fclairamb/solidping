package memlimit

import (
	"math"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
)

func noCgroup() (int64, bool) { return 0, false }
func fixedCgroup(n int64) func() (int64, bool) {
	return func() (int64, bool) { return n, true }
}

func TestDecideMemoryLimit(t *testing.T) {
	t.Parallel()

	// Runtime variables (not consts): int64(float64(const)*ratio) is a constant
	// expression Go rejects for fractional truncation; production multiplies a
	// non-constant int64, so the test mirrors that to match bit-for-bit.
	oneGiB := int64(1024 * 1024 * 1024)
	halfGiB := int64(512 * 1024 * 1024)

	tests := []struct {
		name       string
		cfg        Config
		envValue   string
		detect     func() (int64, bool)
		wantSet    bool
		wantBytes  int64
		wantSource string
	}{
		{
			name:       "native GOMEMLIMIT env wins over everything",
			cfg:        Config{MemoryLimit: "256MiB", Auto: true, Ratio: 0.9},
			envValue:   "512MiB",
			detect:     fixedCgroup(oneGiB),
			wantSet:    false,
			wantSource: "env",
		},
		{
			name:       "env value off is treated as unset",
			cfg:        Config{MemoryLimit: "256MiB"},
			envValue:   "off",
			detect:     noCgroup,
			wantSet:    true,
			wantBytes:  256 * 1024 * 1024,
			wantSource: "config",
		},
		{
			name:       "explicit config human size",
			cfg:        Config{MemoryLimit: "400MiB", Auto: true},
			detect:     fixedCgroup(oneGiB),
			wantSet:    true,
			wantBytes:  400 * 1024 * 1024,
			wantSource: "config",
		},
		{
			name:       "config takes precedence over auto",
			cfg:        Config{MemoryLimit: "100MiB", Auto: true, Ratio: 0.9},
			detect:     fixedCgroup(oneGiB),
			wantSet:    true,
			wantBytes:  100 * 1024 * 1024,
			wantSource: "config",
		},
		{
			name:       "unparseable config falls through to auto",
			cfg:        Config{MemoryLimit: "not-a-size", Auto: true, Ratio: 0.5},
			detect:     fixedCgroup(oneGiB),
			wantSet:    true,
			wantBytes:  oneGiB / 2,
			wantSource: "cgroup",
		},
		{
			name:       "auto derives from cgroup with ratio",
			cfg:        Config{Auto: true, Ratio: 0.9},
			detect:     fixedCgroup(oneGiB),
			wantSet:    true,
			wantBytes:  int64(float64(oneGiB) * 0.9),
			wantSource: "cgroup",
		},
		{
			name:       "auto derives from a smaller cgroup",
			cfg:        Config{Auto: true, Ratio: 0.9},
			detect:     fixedCgroup(halfGiB),
			wantSet:    true,
			wantBytes:  int64(float64(halfGiB) * 0.9),
			wantSource: "cgroup",
		},
		{
			name:       "auto with out-of-range ratio uses default 0.9",
			cfg:        Config{Auto: true, Ratio: 5},
			detect:     fixedCgroup(oneGiB),
			wantSet:    true,
			wantBytes:  int64(float64(oneGiB) * defaultRatio),
			wantSource: "cgroup",
		},
		{
			name:       "auto with zero ratio uses default 0.9",
			cfg:        Config{Auto: true, Ratio: 0},
			detect:     fixedCgroup(oneGiB),
			wantSet:    true,
			wantBytes:  int64(float64(oneGiB) * defaultRatio),
			wantSource: "cgroup",
		},
		{
			name:       "auto with no cgroup limit stays default",
			cfg:        Config{Auto: true, Ratio: 0.9},
			detect:     noCgroup,
			wantSet:    false,
			wantSource: "default",
		},
		{
			name:       "auto disabled and no config stays default",
			cfg:        Config{Auto: false},
			detect:     fixedCgroup(oneGiB),
			wantSet:    false,
			wantSource: "default",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got := decideMemoryLimit(tc.cfg, tc.envValue, tc.detect)

			r.Equal(tc.wantSet, got.set)
			r.Equal(tc.wantSource, got.source)
			if tc.wantSet {
				r.Equal(tc.wantBytes, got.bytes)
			}
		})
	}
}

func TestDecideGCPercent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         Config
		envValue    string
		wantSet     bool
		wantPercent int
		wantSource  string
	}{
		{
			name:        "GOGC env wins",
			cfg:         Config{GCPercent: 50},
			envValue:    "200",
			wantSet:     false,
			wantPercent: 200,
			wantSource:  "env",
		},
		{
			name:        "config value applied",
			cfg:         Config{GCPercent: 60},
			envValue:    "",
			wantSet:     true,
			wantPercent: 60,
			wantSource:  "config",
		},
		{
			name:        "default when nothing set",
			cfg:         Config{GCPercent: 0},
			envValue:    "",
			wantSet:     false,
			wantPercent: defaultGCPercent,
			wantSource:  "default",
		},
		{
			name:        "non-numeric GOGC ignored, default stands",
			cfg:         Config{GCPercent: 0},
			envValue:    "garbage",
			wantSet:     false,
			wantPercent: defaultGCPercent,
			wantSource:  "default",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got := decideGCPercent(tc.cfg, tc.envValue)

			r.Equal(tc.wantSet, got.set)
			r.Equal(tc.wantPercent, got.percent)
			r.Equal(tc.wantSource, got.source)
		})
	}
}

func TestAppliedMemoryLimitHuman(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("unlimited", Applied{MemoryLimitBytes: math.MaxInt64}.MemoryLimitHuman())
	r.Contains(Applied{MemoryLimitBytes: 400 * 1024 * 1024}.MemoryLimitHuman(), "MiB")
}

// TestApplySmoke exercises the global-mutating wrapper end to end. It cannot run
// in parallel: debug.SetMemoryLimit / SetGCPercent change process-wide runtime
// state, so it saves and restores the originals.
//
//nolint:paralleltest // mutates process-global GOMEMLIMIT/GOGC; must be serial
func TestApplySmoke(t *testing.T) {
	r := require.New(t)

	origMem := debug.SetMemoryLimit(-1)
	origGC := debug.SetGCPercent(100)
	t.Cleanup(func() {
		debug.SetMemoryLimit(origMem)
		debug.SetGCPercent(origGC)
	})

	// Inject a fake 1 GiB cgroup limit via the apply() seam so auto-derivation is
	// deterministic regardless of the host (CI may run in a real cgroup). A
	// runtime variable so int64(float64(oneGiB)*0.8) is a non-constant conversion.
	oneGiB := int64(1024 * 1024 * 1024)

	applied := apply(Config{Auto: true, Ratio: 0.8, GCPercent: 70}, fixedCgroup(oneGiB))

	r.Equal("cgroup", applied.MemoryLimitSource)
	r.Equal(int64(float64(oneGiB)*0.8), applied.MemoryLimitBytes)
	r.Equal(int64(float64(oneGiB)*0.8), debug.SetMemoryLimit(-1))
	r.Equal(70, applied.GCPercent)
	r.Equal("config", applied.GCPercentSource)
}
