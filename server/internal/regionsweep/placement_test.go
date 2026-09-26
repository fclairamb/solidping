package regionsweep_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/freshness"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/regionsweep"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// Spec 2026-09-25-06 A2: the region sweep moves AUTOMATICALLY placed checks
// off a region that goes dark, and nothing else.

// TestAutoCheckIsReplacedWhenItsRegionGoesDark: the check is moved within the
// one sweep that sees the region dark, its new job is due now, one
// check.placement_changed event is written, and its org is not told the check
// is blind (it is not). A pinned check in the same region is the control: it
// stays, and its org is told.
func TestAutoCheckIsReplacedWhenItsRegionGoesDark(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.worker(t, "w-paris", healthyRegion)
	env.strand(t)
	auto := env.autoCheck(t, env.acme, "Auto API", darkRegion)
	pinned := env.check(t, env.globex, "Pinned API", darkRegion)
	r.NoError(env.db.UpdateCheck(t.Context(), pinned.UID, &models.CheckUpdate{Regions: &[]string{darkRegion}}))
	env.admin(t, env.acme, "alice@acme.com")
	env.admin(t, env.globex, "bob@globex.com")

	result := env.sweep(t)

	dark := transition(result, darkRegion)
	r.NotNil(dark)
	r.Equal(regionsweep.TransitionDark, dark.Kind)
	r.Len(dark.Replaced, 1)
	r.Equal(checks.PlacementChange{
		OrganizationUID: env.acme.UID, CheckUID: auto.UID, From: darkRegion, To: healthyRegion,
	}, dark.Replaced[0])

	moved := env.reload(t, auto)
	r.Equal([]string{healthyRegion}, moved.Regions, "checks.regions holds the new placement")
	r.Equal(models.PlacementAuto, moved.Placement)

	jobs := env.jobRegions(t, auto)
	r.Len(jobs, 1, "the dark region's job is gone")
	r.Contains(jobs, healthyRegion)
	r.WithinDuration(time.Now(), jobs[healthyRegion], 5*time.Second, "the new job is due now")

	events := env.events(t, env.acme, models.EventTypeCheckPlacementChanged)
	r.Len(events, 1)
	r.Equal(auto.UID, *events[0].CheckUID)
	r.Equal(darkRegion, events[0].Payload[models.PlacementEventPayloadFrom])
	r.Equal(healthyRegion, events[0].Payload[models.PlacementEventPayloadTo])
	r.Equal(models.PlacementReasonRegionOffline, events[0].Payload[models.PlacementEventPayloadReason])

	r.Empty(env.events(t, env.acme, models.EventTypeRegionOffline), "a moved check is not blind")
	r.Empty(env.emails(t, "alice@acme.com", email.TemplateRegionOffline))

	// The pinned control never moves, and its org is told.
	r.Equal([]string{darkRegion}, env.reload(t, pinned).Regions)
	r.Equal(models.PlacementPinned, env.reload(t, pinned).Placement)
	r.Contains(env.jobRegions(t, pinned), darkRegion)
	r.Len(env.events(t, env.globex, models.EventTypeRegionOffline), 1)

	// The following sweeps move nothing more and write no second event.
	for range 2 {
		env.sweep(t)
	}

	r.Len(env.events(t, env.acme, models.EventTypeCheckPlacementChanged), 1)

	// A restart's boot repair keeps the new placement: checks.regions carries
	// it, so the repair (which realigns jobs on checks.regions) has nothing to
	// undo. (It may still heal the pinned fixture, whose raw-inserted job
	// period is spelled differently.)
	_, err := env.checks.ReconcileStaleJobSchedules(t.Context())
	r.NoError(err)
	r.Equal([]string{healthyRegion}, env.reload(t, auto).Regions)

	afterBoot := env.jobRegions(t, auto)
	r.Len(afterBoot, 1)
	r.Contains(afterBoot, healthyRegion)
}

// TestRecoveredRegionMovesNothingBack: once moved, a check stays where it is
// when its old region comes back — no automatic move back, no flapping.
func TestRecoveredRegionMovesNothingBack(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.worker(t, "w-paris", healthyRegion)
	worker := env.strand(t)
	auto := env.autoCheck(t, env.acme, "Auto API", darkRegion)
	// A pinned check keeps the region in use, so it has a recovery to go
	// through.
	env.check(t, env.globex, "Pinned API", darkRegion)

	env.sweep(t)
	r.Equal([]string{healthyRegion}, env.reload(t, auto).Regions)

	r.NoError(env.db.UpdateWorkerHeartbeat(t.Context(), worker.UID, []string{}, ""))

	var recovered *regionsweep.Transition

	for range 3 {
		if tr := transition(env.sweep(t), darkRegion); tr != nil && tr.Kind == regionsweep.TransitionRecovered {
			recovered = tr
		}
	}

	r.NotNil(recovered, "the region went through its recovery")
	r.Equal([]string{healthyRegion}, env.reload(t, auto).Regions, "nothing moves back")
	r.Len(env.jobRegions(t, auto), 1)
	r.Len(env.events(t, env.acme, models.EventTypeCheckPlacementChanged), 1)
}

// TestAutoCheckWithNoHealthyCandidateKeepsItsPlacementAndGoesStale: with no
// healthy region to move to, the placement is kept, the org is told the
// check is blind, and the existing freshness sweep marks it stale — nothing
// here special-cases it. When a candidate turns healthy while the region is
// still dark, the next sweep moves it.
func TestAutoCheckWithNoHealthyCandidateKeepsItsPlacementAndGoesStale(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.strand(t) // paris has no worker at all: no healthy candidate.
	auto := env.autoCheck(t, env.acme, "Auto API", darkRegion)
	env.admin(t, env.acme, "alice@acme.com")

	dark := transition(env.sweep(t), darkRegion)
	r.NotNil(dark)
	r.Empty(dark.Replaced)
	r.Equal([]string{env.acme.UID}, dark.OrgsNotified, "not moved, so blind: the org is told")
	r.Equal([]string{darkRegion}, env.reload(t, auto).Regions, "the placement is kept")
	r.Empty(env.events(t, env.acme, models.EventTypeCheckPlacementChanged))

	// No result arrives from the dark region: the freshness sweep, run as it
	// would be ten minutes later, moves the check to stale on its own.
	incidentsSvc := incidents.NewService(env.db, env.jobs, clock.NewFake(time.Now()), nil)
	marked, err := freshness.NewService(env.db, incidentsSvc, nil, nil).
		SweepStale(t.Context(), time.Now().Add(10*time.Minute))
	r.NoError(err)
	r.Equal(1, marked)
	r.Equal(models.CheckStatusStale, env.reload(t, auto).Status)

	// A candidate becomes healthy while the region is still dark: the next
	// sweep moves the check.
	env.worker(t, "w-paris", healthyRegion)
	env.sweep(t)
	r.Equal([]string{healthyRegion}, env.reload(t, auto).Regions)
	r.Len(env.events(t, env.acme, models.EventTypeCheckPlacementChanged), 1)
}

// TestPlacementFollowsPoolAndCapabilitiesWhenReplacing: a re-placement stays
// inside the check's pool; a pool with no healthy region left moves nothing.
func TestPlacementFollowsPoolWhenReplacing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.worker(t, "w-paris", healthyRegion)
	env.strand(t)

	inPool := env.autoCheck(t, env.acme, "Pool bound", darkRegion)
	r.NoError(env.db.UpdateCheck(t.Context(), inPool.UID, &models.CheckUpdate{RegionPool: &[]string{darkRegion}}))

	env.sweep(t)

	r.Equal([]string{darkRegion}, env.reload(t, inPool).Regions, "paris is outside the pool")
	r.Empty(env.events(t, env.acme, models.EventTypeCheckPlacementChanged))
}

// TestReplaceAutoChecksNeverTouchesPinnedOrPrivate pins the service-level
// guards the sweep relies on.
func TestReplaceAutoChecksNeverTouchesPinnedOrPrivate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.worker(t, "w-paris", healthyRegion)
	pinned := env.check(t, env.acme, "Pinned API", darkRegion)
	r.NoError(env.db.UpdateCheck(t.Context(), pinned.UID, &models.CheckUpdate{Regions: &[]string{darkRegion}}))

	healthy := map[string]bool{healthyRegion: true}

	changes, err := env.checks.ReplaceAutoChecks(t.Context(), checks.ReplacementRequest{
		Region: darkRegion, CheckUIDs: []string{pinned.UID}, Healthy: healthy,
		Reason: models.PlacementReasonRegionOffline,
	})
	r.NoError(err)
	r.Empty(changes, "a pinned check is never moved")
	r.Equal([]string{darkRegion}, env.reload(t, pinned).Regions)

	changes, err = env.checks.ReplaceAutoChecks(t.Context(), checks.ReplacementRequest{
		Region: "@office", CheckUIDs: []string{pinned.UID}, Healthy: healthy,
		Reason: models.PlacementReasonRegionOffline,
	})
	r.NoError(err)
	r.Empty(changes, "a private region is never a re-placement source")
}
