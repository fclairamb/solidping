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
	"math/rand/v2"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksleep/config"
)

// Forced status values accepted by SleepConfig.Status.
// SleepChecker implements the Checker interface for the synthetic sleep check.
type SleepChecker struct{}

// Type returns the check type identifier.
func (c *SleepChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeSleep
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *SleepChecker) Validate(spec *checkerdef.CheckSpec) error {
	return config.ValidateSpec(spec)
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
		Output:   map[string]any{"status": cfg.StatusLabel()},
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
