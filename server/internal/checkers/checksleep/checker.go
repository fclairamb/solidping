// Package checksleep provides a synthetic "sleep" check used as a deterministic
// load generator for the scheduler. It performs no network I/O: it sleeps for a
// configured number of milliseconds (optionally jittered) and returns a
// configurable status. Because its cost equals its configured sleep, it lets us
// place a job at any exact point on the fast↔slow axis and directly exercise the
// cost/delay EWMA and the cost-aware timeout. It is synthetic/testing only and
// must NOT be counted in the customer "N check types" tally.
package checksleep

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// maxSleepMs is the upper bound on the configured sleep, in milliseconds.
	maxSleepMs = 120000

	// slugMaxLen bounds the auto-generated slug length.
	slugMaxLen = 50
)

// Forced status values accepted by SleepConfig.Status.
const (
	statusUp      = "up"
	statusDown    = "down"
	statusTimeout = "timeout"
	statusError   = "error"
)

// SleepChecker implements the Checker interface for the synthetic sleep check.
type SleepChecker struct{}

// Type returns the check type identifier.
func (c *SleepChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeSleep
}

// Validate checks if the configuration is valid. It performs no network I/O.
func (c *SleepChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &SleepConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if cfg.SleepMs <= 0 {
		return checkerdef.NewConfigError("sleep_ms", "is required and must be > 0")
	}

	if cfg.SleepMs > maxSleepMs {
		return checkerdef.NewConfigErrorf("sleep_ms", "must be <= %d, got %d", maxSleepMs, cfg.SleepMs)
	}

	if cfg.JitterMs < 0 {
		return checkerdef.NewConfigError("jitter_ms", "must be >= 0")
	}

	if cfg.JitterMs >= cfg.SleepMs {
		return checkerdef.NewConfigErrorf("jitter_ms", "must be < sleep_ms (%d), got %d", cfg.SleepMs, cfg.JitterMs)
	}

	switch cfg.Status {
	case "", statusUp, statusDown, statusTimeout, statusError:
		// valid
	default:
		return checkerdef.NewConfigErrorf("status", "must be one of up|down|timeout|error, got %q", cfg.Status)
	}

	if spec.Name == "" {
		spec.Name = fmt.Sprintf("sleep-%dms", cfg.SleepMs)
	}

	if spec.Slug == "" {
		spec.Slug = truncateSlug(fmt.Sprintf("sleep-%dms", cfg.SleepMs))
	}

	return nil
}

// Execute sleeps for the configured (optionally jittered) duration, honoring
// ctx so the cost-aware timeout can interrupt it, then returns the configured
// (or default up) status.
func (c *SleepChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*SleepConfig](config)
	if err != nil {
		return nil, err
	}

	sleepFor := sleepDuration(cfg)

	start := time.Now()

	timer := time.NewTimer(sleepFor)
	defer timer.Stop()

	select {
	case <-timer.C:
		// Slept the full duration; fall through to the forced/default status.
	case <-ctx.Done():
		// Interrupted by the cost-aware timeout (or cancellation). Report a
		// timeout pinned to the actual time slept — this reproduces a
		// timing-out endpoint deterministically (spec D2).
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Metrics:  map[string]any{configKeySleepMs: sleepFor.Milliseconds()},
			Output: map[string]any{
				checkerdef.OutputKeyError: "sleep interrupted by context (cost-aware timeout)",
			},
		}, nil
	}

	slept := time.Since(start)

	return &checkerdef.Result{
		Status:   forcedStatus(cfg.Status),
		Duration: slept,
		Metrics:  map[string]any{configKeySleepMs: sleepFor.Milliseconds()},
		Output:   map[string]any{"status": cfg.statusLabel()},
	}, nil
}

// sleepDuration computes the target sleep, applying a uniform ± jitter when
// configured. The result is clamped to be non-negative.
func sleepDuration(cfg *SleepConfig) time.Duration {
	totalMs := cfg.SleepMs

	if cfg.JitterMs > 0 {
		// Uniform jitter in [-JitterMs, +JitterMs]. math/rand/v2 is fine here —
		// jitter is non-cryptographic.
		totalMs += rand.IntN(2*cfg.JitterMs+1) - cfg.JitterMs
	}

	if totalMs < 0 {
		totalMs = 0
	}

	return time.Duration(totalMs) * time.Millisecond
}

// forcedStatus maps the configured status string to a checkerdef.Status,
// defaulting to Up.
func forcedStatus(status string) checkerdef.Status {
	switch status {
	case statusDown:
		return checkerdef.StatusDown
	case statusTimeout:
		return checkerdef.StatusTimeout
	case statusError:
		return checkerdef.StatusError
	default:
		return checkerdef.StatusUp
	}
}

// statusLabel returns the effective status label (defaulting to up).
func (c *SleepConfig) statusLabel() string {
	if c.Status == "" {
		return statusUp
	}

	return c.Status
}

// truncateSlug bounds a slug to slugMaxLen runes.
func truncateSlug(s string) string {
	if len(s) <= slugMaxLen {
		return s
	}

	return s[:slugMaxLen]
}
