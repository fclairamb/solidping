package regions

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// LivenessCutoff is the oldest last-seen instant that still counts as live at
// now: now minus WorkerLivenessWindow.
func LivenessCutoff(now time.Time) time.Time {
	return now.Add(-WorkerLivenessWindow)
}

// IsAgentLive is THE per-agent liveness rule for a private location (spec
// 2026-09-25-01): the agent is active (a revoked agent can no longer
// authenticate, let alone claim) and was seen within the liveness window,
// inclusive.
//
// Region health, the private-location liveness monitor and the Private
// Locations page all read it (spec 2026-09-25-05), so an agent can never be
// "online" on one surface and "offline" on another.
func IsAgentLive(status string, lastSeenAt *time.Time, liveCutoff time.Time) bool {
	return status == models.AgentStatusActive && lastSeenAt != nil && !lastSeenAt.Before(liveCutoff)
}
