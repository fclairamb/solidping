// Package memlimit applies process-wide Go runtime memory guardrails — the
// GOMEMLIMIT soft heap cap and the GOGC heap-growth target — from
// configuration, optionally deriving the soft cap from the container's cgroup
// memory limit so a self-hosted/Kubernetes deployment is protected from the OOM
// killer without any operator action.
//
// Precedence (least surprising for operators who know Go): a native GOMEMLIMIT
// or GOGC environment variable always wins, because the Go runtime applied it at
// startup and an operator who set it clearly meant it. Only when the raw env var
// is absent do the SolidPing-native config knobs (and cgroup auto-derivation)
// take effect.
package memlimit

import (
	"math"
	"os"
	"runtime/debug"
	"strconv"

	units "github.com/docker/go-units"
)

// unlimited is the value debug.SetMemoryLimit reports when no soft cap is set.
const unlimited = math.MaxInt64

// defaultGCPercent is Go's built-in GOGC default, reported when neither GOGC nor
// the config knob overrides it.
const defaultGCPercent = 100

// defaultRatio is the headroom factor applied to a detected cgroup limit when
// the configured ratio is out of range.
const defaultRatio = 0.9

// envValueOff is how Go spells "disabled" for GOMEMLIMIT/GOGC env vars.
const envValueOff = "off"

// Source labels for how a guardrail value was decided.
const (
	sourceEnv     = "env"
	sourceConfig  = "config"
	sourceCgroup  = "cgroup"
	sourceDefault = "default"
)

// Config controls the runtime memory guardrails.
type Config struct {
	// MemoryLimit, when non-empty, is the explicit GOMEMLIMIT soft cap. It
	// accepts human sizes ("400MiB", "1GiB") or a raw byte count. It takes
	// precedence over Auto but not over a native GOMEMLIMIT env var.
	MemoryLimit string

	// Auto derives the soft cap from the cgroup memory limit × Ratio when
	// MemoryLimit is empty and no native GOMEMLIMIT env var is set.
	Auto bool

	// Ratio is the fraction of the detected cgroup limit to use as the soft cap
	// (headroom for stacks, off-heap, and the runtime). Clamped to (0,1]; a
	// non-positive or >1 value falls back to defaultRatio.
	Ratio float64

	// GCPercent maps to debug.SetGCPercent when > 0; 0 leaves the runtime
	// default (or a native GOGC env var) untouched.
	GCPercent int
}

// Applied reports the effective guardrails after Apply, for logging and the
// /api/mgmt/memory snapshot.
type Applied struct {
	// MemoryLimitBytes is the effective GOMEMLIMIT. math.MaxInt64 means no cap.
	MemoryLimitBytes int64
	// MemoryLimitSource is how the cap was decided: "env", "config", "cgroup",
	// or "default".
	MemoryLimitSource string
	// GCPercent is the effective GOGC.
	GCPercent int
	// GCPercentSource is how GOGC was decided: "env", "config", or "default".
	GCPercentSource string
}

// MemoryLimitHuman renders the effective cap for logs ("unlimited" when uncapped).
func (a Applied) MemoryLimitHuman() string {
	if a.MemoryLimitBytes >= unlimited {
		return "unlimited"
	}
	return units.BytesSize(float64(a.MemoryLimitBytes))
}

// Apply sets GOMEMLIMIT and GOGC according to cfg and the environment, returning
// the effective values. Call once at startup.
func Apply(cfg Config) Applied {
	return apply(cfg, detectCgroupMemoryLimitOS)
}

// apply is the testable core: it takes the cgroup detector explicitly so a test
// can inject a deterministic limit. The decision logic lives in the pure decide*
// helpers; this wrapper performs the global runtime mutation.
func apply(cfg Config, detect func() (int64, bool)) Applied {
	mem := decideMemoryLimit(cfg, os.Getenv("GOMEMLIMIT"), detect)
	if mem.set {
		debug.SetMemoryLimit(mem.bytes)
	}
	// Read back the effective limit: this reflects a native GOMEMLIMIT env var,
	// our just-set value, or the runtime default — without changing it.
	effBytes := debug.SetMemoryLimit(-1)

	gcPlan := decideGCPercent(cfg, os.Getenv("GOGC"))
	if gcPlan.set {
		debug.SetGCPercent(gcPlan.percent)
	}

	return Applied{
		MemoryLimitBytes:  effBytes,
		MemoryLimitSource: mem.source,
		GCPercent:         gcPlan.percent,
		GCPercentSource:   gcPlan.source,
	}
}

// memDecision is the resolved soft-cap plan from decideMemoryLimit.
type memDecision struct {
	set    bool   // whether to call debug.SetMemoryLimit(bytes)
	bytes  int64  // the cap to set (valid only when set)
	source string // sourceEnv | sourceConfig | sourceCgroup | sourceDefault
}

// decideMemoryLimit resolves the soft cap from config, a (possibly empty) native
// GOMEMLIMIT value, and a cgroup detector. Pure: no global mutation, no env
// reads — fully unit-testable.
func decideMemoryLimit(cfg Config, envGoMemLimit string, detect func() (int64, bool)) memDecision {
	if isEnvSet(envGoMemLimit) {
		return memDecision{source: sourceEnv}
	}
	if d, ok := memLimitFromConfig(cfg); ok {
		return d
	}
	if d, ok := memLimitFromCgroup(cfg, detect); ok {
		return d
	}
	return memDecision{source: sourceDefault}
}

// memLimitFromConfig parses an explicit cfg.MemoryLimit. An unparseable value
// returns (_, false) so the caller falls through to auto/default rather than
// crashing the process over a misconfigured knob.
func memLimitFromConfig(cfg Config) (memDecision, bool) {
	if cfg.MemoryLimit == "" {
		return memDecision{}, false
	}
	bytes, err := units.RAMInBytes(cfg.MemoryLimit)
	if err != nil || bytes <= 0 {
		return memDecision{}, false
	}
	return memDecision{set: true, bytes: bytes, source: sourceConfig}, true
}

// memLimitFromCgroup derives the cap from the detected cgroup limit × ratio.
func memLimitFromCgroup(cfg Config, detect func() (int64, bool)) (memDecision, bool) {
	if !cfg.Auto {
		return memDecision{}, false
	}
	limit, ok := detect()
	if !ok || limit <= 0 {
		return memDecision{}, false
	}
	ratio := cfg.Ratio
	if ratio <= 0 || ratio > 1 {
		ratio = defaultRatio
	}
	bytes := int64(float64(limit) * ratio)
	if bytes <= 0 {
		return memDecision{}, false
	}
	return memDecision{set: true, bytes: bytes, source: sourceCgroup}, true
}

// gcDecision is the resolved GOGC plan from decideGCPercent.
type gcDecision struct {
	set     bool   // whether to call debug.SetGCPercent(percent)
	percent int    // the effective GOGC value
	source  string // sourceEnv | sourceConfig | sourceDefault
}

// decideGCPercent resolves GOGC from config and a (possibly empty) native GOGC
// value. Pure: no global mutation.
func decideGCPercent(cfg Config, envGOGC string) gcDecision {
	if n, ok := parseEnvInt(envGOGC); ok {
		return gcDecision{percent: n, source: sourceEnv}
	}
	if cfg.GCPercent > 0 {
		return gcDecision{set: true, percent: cfg.GCPercent, source: sourceConfig}
	}
	return gcDecision{percent: defaultGCPercent, source: sourceDefault}
}

// isEnvSet reports whether an env value is meaningfully set (Go treats "off" as
// "disabled" for GOMEMLIMIT).
func isEnvSet(v string) bool {
	return v != "" && v != envValueOff
}

// parseEnvInt parses an integer env value, treating empty/"off" as unset.
func parseEnvInt(v string) (int, bool) {
	if v == "" || v == envValueOff {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}
