package jobtypes

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// An escalation cycle is scheduled the moment an incident opens, but a
// dependency rollup can attach that incident to a hard parent MINUTES later —
// the parent's own probe usually confirms after its dependents' do (spec
// 2026-08-24-15). The queued steps therefore have to re-read paging_suppressed
// at FIRE time, not just at schedule time, or retroactive suppression cannot
// stop a storm that is already in flight.
func TestIncidentNeedsPagingHonoursPagingSuppressed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Date(2026, 8, 23, 23, 52, 0, 0, time.UTC)
	later := now.Add(10 * time.Minute)

	// Positive control: an untouched active incident DOES still page, so the
	// assertions below cannot pass because the helper returns false for
	// everything.
	r.True(incidentNeedsPaging(&models.Incident{}, now),
		"an open, unhandled, unsuppressed incident still pages")

	r.False(incidentNeedsPaging(&models.Incident{PagingSuppressed: true}, now),
		"a retroactively rolled-up child must stop firing its pending steps")

	// The pre-existing gates are unchanged.
	r.False(incidentNeedsPaging(&models.Incident{AcknowledgedAt: &now}, now))
	r.False(incidentNeedsPaging(&models.Incident{ResolvedAt: &now}, now))
	r.False(incidentNeedsPaging(&models.Incident{SnoozedUntil: &later}, now))
	r.True(incidentNeedsPaging(&models.Incident{SnoozedUntil: &now}, later),
		"an expired snooze pages again")
}
