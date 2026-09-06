package checkheartbeat

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample heartbeat check configurations.
//
// ONLY the Demo catalog gets one (spec 2026-09-06-02). A heartbeat is a
// passive check: it goes down unless something out there pings it, so seeding
// one into a fresh self-hosted install's `default` org would hand a new
// operator a red check on their first login with nothing to fix. In the demo
// that is exactly what we want — an unpinged heartbeat is the clearest possible
// illustration of what "dead man's switch monitoring" means, and the demo's
// whole job is to show the product's shapes.
//
// The token is left empty on purpose: the create path mints one, and a sample
// that shipped a fixed token would publish it.
func (c *HeartbeatChecker) GetSampleConfigs(opts *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	if opts == nil || opts.Type != checkerdef.Demo {
		return nil
	}

	return []checkerdef.CheckSpec{
		{
			Name:   "Nightly backup (heartbeat)",
			Slug:   "demo-heartbeat-backup",
			Period: time.Hour,
			Config: map[string]any{},
		},
	}
}
