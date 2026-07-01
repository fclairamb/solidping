package checksleep

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	sampleFastSleepMs = 200  // fast synthetic load
	sampleSlowSleepMs = 8000 // slow synthetic load
)

// GetSampleConfigs returns sample synthetic sleep check configurations: one
// fast, one slow. These exist so the scheduler can be driven with a controlled
// fast/slow mix; the sleep type is synthetic/testing only.
func (c *SleepChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "sleep-200ms (fast)",
			Slug:   "sleep-fast",
			Period: time.Minute,
			Config: (&SleepConfig{
				SleepMs: sampleFastSleepMs,
			}).GetConfig(),
		},
		{
			Name:   "sleep-8000ms (slow)",
			Slug:   "sleep-slow",
			Period: time.Minute,
			Config: (&SleepConfig{
				SleepMs: sampleSlowSleepMs,
			}).GetConfig(),
		},
	}
}
