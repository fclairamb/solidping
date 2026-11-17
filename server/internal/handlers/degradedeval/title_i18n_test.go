package degradedeval

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// TitleI18n must follow Title branch for branch, with the same rounded
// numbers, or the dashboard would show a different title than the mail.
func TestTitleI18nFollowsTitle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	check := models.NewCheck("org", "api", "http")

	availability := 66.666
	withAvailability := &incidents.DegradedSnapshot{
		FailuresFired: true, FailureMatches: 2, FailureSlots: 6, AvailabilityPct: &availability,
	}
	r.Equal("api is degraded: 2 failures in the last 6 probes (66.7% available)", Title(check, withAvailability))

	recipe := TitleI18n(check, withAvailability)
	r.Equal(incidents.TitleKeyDegradedFailuresAvailability, recipe.Key)
	r.Equal("api", recipe.Params.Name)
	r.Equal(2, *recipe.Params.Failures)
	r.Equal(6, *recipe.Params.Probes)
	r.InDelta(66.7, *recipe.Params.Availability, 1e-9)

	failures := &incidents.DegradedSnapshot{FailuresFired: true, FailureMatches: 2, FailureSlots: 6}
	r.Equal(incidents.TitleKeyDegradedFailures, TitleI18n(check, failures).Key)
	r.Nil(TitleI18n(check, failures).Params.Availability)

	slow := &incidents.DegradedSnapshot{SlowMatches: 3, SlowSlots: 6, SlowThreshold: 800}
	recipe = TitleI18n(check, slow)
	r.Equal(incidents.TitleKeyDegradedSlow, recipe.Key)
	r.Equal(3, *recipe.Params.Slow)
	r.Equal(800, *recipe.Params.ThresholdMs)
}
