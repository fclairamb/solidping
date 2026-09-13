package checks_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// periodSatisfiesTypeBound mirrors the bound checks.validatePeriodForType
// enforces on a proposed period, using only checkerdef's exported metadata —
// this is a black-box test (package checks_test), so it cannot call that
// unexported function directly. It exists to check the OUTPUT of CreateCheck
// against the same rule the server would apply if the caller had typed this
// period explicitly, which is exactly the invariant a no-period create must
// not violate (spec 2026-09-11-07): a check must never come out of CreateCheck
// carrying a period the server would itself reject on the way in.
func periodSatisfiesTypeBound(checkType string, period time.Duration) bool {
	if checkerdef.CheckType(checkType) == checkerdef.CheckTypeSleep {
		return true // validatePeriodForType exempts the synthetic sleep type unconditionally.
	}

	minPeriod := checkerdef.GlobalMinPeriod

	var maxPeriod time.Duration

	if meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckType(checkType)); meta != nil {
		if meta.MinPeriod > 0 {
			minPeriod = meta.MinPeriod
		}

		maxPeriod = meta.MaxPeriod
	}

	if period < minPeriod {
		return false
	}

	if maxPeriod > 0 && period > maxPeriod {
		return false
	}

	return true
}

// TestCreateWithNoPeriodNeverStoresBelowTypeFloor is the headline test for
// spec 2026-09-11-07: for every check type, CreateCheck with NO period
// supplied must store a period that satisfies that type's OWN bound — never a
// flat value that happens to be fine for some types and below the floor for
// others. Before the fix this failed for ssl/domain/dnsbl (and would have for
// js/browser too, had their floor sat above one minute): CreateCheck stored
// models.NewCheck's flat 1-minute constant regardless of type.
func TestCreateWithNoPeriodNeverStoresBelowTypeFloor(t *testing.T) {
	t.Parallel()

	rig := newRoundTripRig(t)
	exercised := map[string]bool{}

	// domain has no CheckerSamplesProvider (registry.GetAllSampleConfigs
	// returns nothing for it), yet it is one of the 5 types this whole spec
	// is about — so it gets a minimal fallback config here rather than being
	// silently dropped from coverage.
	extraSamples := map[checkerdef.CheckType][]checkerdef.CheckSpec{
		checkerdef.CheckTypeDomain: {{Config: map[string]any{"domain": "example.com"}}},
	}

	allSamples := registry.GetAllSampleConfigs(nil)

	for _, checkType := range checkerdef.ListCheckTypes(nil) {
		samples := allSamples[checkType]
		if len(samples) == 0 {
			samples = extraSamples[checkType]
		}

		for i, sample := range samples {
			r := require.New(t)
			slug := fmt.Sprintf("noperiod-%s-%d", strings.ReplaceAll(string(checkType), "_", "-"), i)

			resp, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug, checks.CreateCheckRequest{
				Name: "No period " + slug, Slug: slug, Type: string(checkType), Config: sample.Config,
			})
			if err != nil {
				// Some sample configs need live infrastructure (a tunnel, an
				// integration reference) this offline rig cannot provide —
				// skipped, same as roundTripRig.create. What matters is that
				// every type that CAN be created offline is checked below.
				t.Logf("skipping %s/%s: not creatable offline: %v", checkType, slug, err)

				continue
			}

			exercised[string(checkType)] = true

			r.NotNil(resp.Period, "a created check must always carry a resolved period")

			var stored timeutils.Duration
			r.NoError(stored.Scan(*resp.Period))

			r.True(periodSatisfiesTypeBound(string(checkType), time.Duration(stored)),
				"%s (%s): stored period %s violates its own type's bound",
				checkType, slug, time.Duration(stored))
		}
	}

	// Positive control: the floor-declaring types this spec is actually about
	// must have been exercised by the loop above, or the assertions inside it
	// proved nothing about them.
	for _, floorType := range []string{"ssl", "domain", "dnsbl", "js", "browser"} {
		require.True(t, exercised[floorType],
			"%s must be creatable offline for this test to mean anything", floorType)
	}
}
