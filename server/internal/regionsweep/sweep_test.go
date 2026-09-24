package regionsweep_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/regionoutage"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/regionsweep"
)

// TestRegionGoesDarkNotifiesOrgAndOperatorOnce is the 2026-09-24 shape: one
// check pinned to a region whose only worker died. The org and the operator
// are each told exactly once, and the following sweeps stay quiet.
func TestRegionGoesDarkNotifiesOrgAndOperatorOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.worker(t, "w-paris", healthyRegion)
	worker := env.strand(t)
	env.check(t, env.acme, "Pinned API", darkRegion)
	env.admin(t, env.acme, "alice@acme.com")
	env.operator(t, true)

	result := env.sweep(t)

	dark := transition(result, darkRegion)
	r.NotNil(dark, "one pinned check is enough: there is no job-count floor")
	r.Equal(regionsweep.TransitionDark, dark.Kind)
	r.Equal([]string{env.acme.UID}, dark.OrgsNotified)
	r.True(dark.OperatorNotified)
	r.Nil(transition(result, healthyRegion))

	marker := env.marker(t, darkRegion)
	r.True(marker.IsDark())
	r.Equal([]string{env.acme.UID}, marker.NotifiedOrgs)
	r.WithinDuration(time.Now().Add(-10*time.Minute), marker.Since, 5*time.Second,
		"since is the last worker beat, not the time the sweep noticed")

	orgMails := env.emails(t, "alice@acme.com", email.TemplateRegionOffline, "Pinned API")
	r.Len(orgMails, 1)
	r.Len(env.events(t, env.acme, models.EventTypeRegionOffline), 1)
	r.Len(env.emails(t, operatorEmail, "lauterbourg is dark"), 1)

	for range 3 {
		result = env.sweep(t)
		r.Empty(result.Transitions)
	}

	r.Len(env.emails(t, "alice@acme.com", email.TemplateRegionOffline), 1, "no second org notice")
	r.Len(env.events(t, env.acme, models.EventTypeRegionOffline), 1)
	r.Len(env.emails(t, operatorEmail, "lauterbourg is dark"), 1, "no second operator notice")

	_ = worker
}

// TestReducedCheckGetsNoOrgNotice: a check running from a dark AND a live
// region is reduced, not blind. Its org gets no push notice; the operator is
// still told.
func TestReducedCheckGetsNoOrgNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.worker(t, "w-paris", healthyRegion)
	env.strand(t)
	env.check(t, env.acme, "Multi", darkRegion, healthyRegion)
	env.admin(t, env.acme, "alice@acme.com")
	env.operator(t, true)

	result := env.sweep(t)

	dark := transition(result, darkRegion)
	r.NotNil(dark)
	r.Empty(dark.OrgsNotified)
	r.True(dark.OperatorNotified)
	r.Empty(env.emails(t, "alice@acme.com", email.TemplateRegionOffline))
	r.Empty(env.events(t, env.acme, models.EventTypeRegionOffline))
	r.True(env.marker(t, darkRegion).IsDark(), "the region is still dark for the operator and dash0")
}

// TestShortBeatGapCausesNoTransition: a worker that missed beats for less than
// the 5-minute liveness window is still live — no transition, no marker.
func TestShortBeatGapCausesNoTransition(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	worker := env.worker(t, "w-"+darkRegion, darkRegion)
	env.lastBeat(t, worker, 4*time.Minute)
	env.check(t, env.acme, "Pinned API", darkRegion)
	env.admin(t, env.acme, "alice@acme.com")

	result := env.sweep(t)
	r.Empty(result.Transitions)
	r.Nil(env.marker(t, darkRegion))
	r.Empty(env.emails(t, "alice@acme.com"))

	// Positive control: past the window, the same fixture goes dark.
	env.lastBeat(t, worker, 6*time.Minute)
	r.NotNil(transition(env.sweep(t), darkRegion))
}

// TestRecoveryNotifiesExactlyTheMarkerOrgsOnce: recovery needs two healthy
// sweeps, then tells exactly the orgs recorded in the marker — not an org
// whose check was only reduced — and only once.
func TestRecoveryNotifiesExactlyTheMarkerOrgsOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.worker(t, "w-paris", healthyRegion)
	worker := env.strand(t)
	env.check(t, env.acme, "Pinned API", darkRegion)
	env.check(t, env.globex, "Multi", darkRegion, healthyRegion)
	env.admin(t, env.acme, "alice@acme.com")
	env.admin(t, env.globex, "bob@globex.com")
	env.operator(t, true)

	env.sweep(t)
	r.Equal([]string{env.acme.UID}, env.marker(t, darkRegion).NotifiedOrgs)

	// The worker comes back.
	r.NoError(env.db.UpdateWorkerHeartbeat(t.Context(), worker.UID, []string{}, ""))

	result := env.sweep(t)
	r.Empty(result.Transitions, "one healthy sweep is not a recovery")
	r.Equal(1, env.marker(t, darkRegion).HealthyStreak)
	r.Empty(env.emails(t, "alice@acme.com", email.TemplateRegionRecovered))

	result = env.sweep(t)
	recovered := transition(result, darkRegion)
	r.NotNil(recovered)
	r.Equal(regionsweep.TransitionRecovered, recovered.Kind)
	r.Equal([]string{env.acme.UID}, recovered.OrgsNotified)
	r.True(recovered.OperatorNotified)
	r.Nil(env.marker(t, darkRegion))

	r.Len(env.emails(t, "alice@acme.com", email.TemplateRegionRecovered), 1)
	r.Len(env.events(t, env.acme, models.EventTypeRegionRecovered), 1)
	r.Empty(env.emails(t, "bob@globex.com"), "a reduced-only org was never told, so it is not told of the recovery")
	r.Empty(env.events(t, env.globex, models.EventTypeRegionRecovered))
	r.Len(env.emails(t, operatorEmail, "lauterbourg recovered"), 1)

	for range 2 {
		r.Empty(env.sweep(t).Transitions)
	}

	r.Len(env.emails(t, "alice@acme.com", email.TemplateRegionRecovered), 1, "a recovery is announced once")
}

// TestFlapResetsTheRecoveryStreak: dark again between the two healthy sweeps
// restarts the count and never re-notifies.
func TestFlapResetsTheRecoveryStreak(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	worker := env.strand(t)
	env.check(t, env.acme, "Pinned API", darkRegion)
	env.admin(t, env.acme, "alice@acme.com")

	env.sweep(t)

	r.NoError(env.db.UpdateWorkerHeartbeat(t.Context(), worker.UID, []string{}, ""))
	r.Empty(env.sweep(t).Transitions)

	env.lastBeat(t, worker, 10*time.Minute)
	r.Empty(env.sweep(t).Transitions, "still the same outage")
	r.Equal(0, env.marker(t, darkRegion).HealthyStreak)
	r.Len(env.emails(t, "alice@acme.com", email.TemplateRegionOffline), 1)
}

// TestCheckBecomesBlindMidOutageNotifiesOncePerRegion: a check spans two
// regions. Region 1 goes dark first — the check is still reduced (region 2
// lives), so the org is not told yet. When region 2 also goes dark, the check
// is blind for the first time. apply.go's stayUnhealthy re-classifies region
// 1's ongoing outage on every dark sweep and finds the org newly eligible
// (spec's "an org not yet in the marker"), while region 2's own goDark
// transition independently reaches the same conclusion. Each region's marker
// tracks its own notified-orgs list, so this is one notice per region's
// outage, not a single merged one — documented here as intended behavior
// (the same mechanism a check whose two regions go dark in the very same
// sweep would hit).
func TestCheckBecomesBlindMidOutageNotifiesOncePerRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	const secondRegion = "berlin"

	r.NoError(env.db.SetSystemParameter(t.Context(), regions.ParamRegions, []regions.RegionDefinition{
		{Slug: healthyRegion, Name: "Paris"},
		{Slug: darkRegion, Name: "Lauterbourg"},
		{Slug: secondRegion, Name: "Berlin"},
	}, false))

	env.strand(t)
	worker2 := env.worker(t, "w-"+secondRegion, secondRegion)
	env.check(t, env.acme, "Two Region", darkRegion, secondRegion)
	env.admin(t, env.acme, "alice@acme.com")

	// Region 1 alone goes dark: the check still runs from region 2, so it is
	// reduced and the org hears nothing.
	first := transition(env.sweep(t), darkRegion)
	r.NotNil(first)
	r.Empty(first.OrgsNotified, "reduced, not blind: no notice yet")
	r.Empty(env.emails(t, "alice@acme.com", email.TemplateRegionOffline))

	// Region 2 also goes dark: the check is blind for the first time.
	env.lastBeat(t, worker2, 10*time.Minute)

	second := transition(env.sweep(t), secondRegion)
	r.NotNil(second)
	r.Equal(regionsweep.TransitionDark, second.Kind)
	r.Equal([]string{env.acme.UID}, second.OrgsNotified,
		"region 2's own transition finds the now-blind check")

	// The org gets one notice per region's outage: region 1's ongoing outage
	// (caught by stayUnhealthy re-classifying) and region 2's own transition.
	offlineMails := env.emails(t, "alice@acme.com", email.TemplateRegionOffline)
	r.Len(offlineMails, 2, "one notice per region, not a single merged notice")

	events := env.events(t, env.acme, models.EventTypeRegionOffline)
	r.Len(events, 2)

	regionsNotified := make([]string, 0, len(events))
	for _, ev := range events {
		regionsNotified = append(regionsNotified, ev.Payload["region"].(string)) //nolint:forcetypeassert // test
	}

	r.ElementsMatch([]string{darkRegion, secondRegion}, regionsNotified)

	marker1 := env.marker(t, darkRegion)
	r.Equal([]string{env.acme.UID}, marker1.NotifiedOrgs)
	r.WithinDuration(time.Now().Add(-10*time.Minute), marker1.Since, 5*time.Second,
		"region 1's notice is dated to when region 1 itself went dark")

	marker2 := env.marker(t, secondRegion)
	r.Equal([]string{env.acme.UID}, marker2.NotifiedOrgs)
	r.WithinDuration(time.Now().Add(-10*time.Minute), marker2.Since, 5*time.Second,
		"region 2's notice is dated to its own last worker beat too")

	// A further sweep with nothing changed sends nothing more.
	r.Empty(env.sweep(t).Transitions)
	r.Len(env.emails(t, "alice@acme.com", email.TemplateRegionOffline), 2, "no repeat notice")
}

// TestTwoOrgsOneRegion: each org gets its own notice, listing only its own
// checks.
func TestTwoOrgsOneRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.strand(t)
	env.check(t, env.acme, "Acme API", darkRegion)
	env.check(t, env.acme, "Acme Web", darkRegion)
	env.check(t, env.globex, "Globex Shop", darkRegion)
	env.admin(t, env.acme, "alice@acme.com")
	env.admin(t, env.globex, "bob@globex.com")

	dark := transition(env.sweep(t), darkRegion)
	r.NotNil(dark)
	r.ElementsMatch([]string{env.acme.UID, env.globex.UID}, dark.OrgsNotified)
	r.False(dark.OperatorNotified, "no platform_watchdog recipients configured")

	acmeMails := env.emails(t, "alice@acme.com", email.TemplateRegionOffline)
	r.Len(acmeMails, 1)
	r.Len(env.emails(t, "alice@acme.com", "Acme API", "Acme Web"), 1)
	r.Empty(env.emails(t, "alice@acme.com", "Globex Shop"))

	r.Len(env.emails(t, "bob@globex.com", email.TemplateRegionOffline, "Globex Shop"), 1)
	r.Empty(env.emails(t, "bob@globex.com", "Acme API"))

	acmeEvents := env.events(t, env.acme, models.EventTypeRegionOffline)
	r.Len(acmeEvents, 1)
	r.EqualValues(2, acmeEvents[0].Payload["blindChecks"])

	globexEvents := env.events(t, env.globex, models.EventTypeRegionOffline)
	r.Len(globexEvents, 1)
	r.EqualValues(1, globexEvents[0].Payload["blindChecks"])
}

// TestPrivateRegionIsNeverReported: a private `@` region with work and no
// live agent is a ghost for RegionHealth, but never a transition, a marker or
// a notice for this sweep.
func TestPrivateRegionIsNeverReported(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.check(t, env.acme, "On-prem", "@datacenter")
	env.admin(t, env.acme, "alice@acme.com")
	env.operator(t, true)

	report, err := env.checks.RegionHealth(t.Context())
	r.NoError(err)
	r.Equal(1, report.GhostCount, "positive control: the private region really is dark")

	result := env.sweep(t)
	r.Empty(result.Transitions)
	r.Nil(env.marker(t, "@datacenter"))

	markers, err := regionoutage.List(t.Context(), env.db)
	r.NoError(err)
	r.Empty(markers)
	r.Empty(env.emails(t, "alice@acme.com"))
	r.Empty(env.emails(t, operatorEmail))
}

// TestPrivateRegionDownMakesACheckBlind: a check pinned to a dark cloud region
// and to a private region with no live agent runs nowhere — blind.
func TestPrivateRegionDownMakesACheckBlind(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.strand(t)
	env.check(t, env.acme, "Hybrid", darkRegion, "@datacenter")
	env.admin(t, env.acme, "alice@acme.com")

	dark := transition(env.sweep(t), darkRegion)
	r.NotNil(dark)
	r.Equal([]string{env.acme.UID}, dark.OrgsNotified)
}

// TestStalledRegionIsOperatorOnly: live workers that do not claim — jobs
// overdue past max(2 × period, 5 min) — page the operator, never an org.
func TestStalledRegionIsOperatorOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.worker(t, "w-"+darkRegion, darkRegion)
	env.checkScheduled(t, env.acme, "Late", time.Now().Add(-10*time.Minute), darkRegion)
	env.checkScheduled(t, env.acme, "Barely late", time.Now().Add(-90*time.Second), healthyRegion)
	env.worker(t, "w-paris", healthyRegion)
	env.admin(t, env.acme, "alice@acme.com")
	env.operator(t, true)

	result := env.sweep(t)

	stalled := transition(result, darkRegion)
	r.NotNil(stalled)
	r.Equal(regionsweep.TransitionStalled, stalled.Kind)
	r.True(stalled.OperatorNotified)
	r.Empty(stalled.OrgsNotified)
	r.Nil(transition(result, healthyRegion), "90 seconds late is the scheduler breathing, not a stall")

	r.Equal(regionoutage.PhaseStalled, env.marker(t, darkRegion).Phase)
	r.False(env.marker(t, darkRegion).IsDark(), "stalled is not offline for dash0")
	r.Empty(env.emails(t, "alice@acme.com"))
	r.Len(env.emails(t, operatorEmail, "lauterbourg is stalled"), 1)
}

// TestInternalChecksAreLeftOut: internal (self-stats) checks never make an
// org blind — nobody is waiting on them.
func TestInternalChecksAreLeftOut(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.strand(t)
	check := env.check(t, env.acme, "Self stats", darkRegion)

	_, err := env.db.DB().NewUpdate().
		Model((*models.Check)(nil)).
		Set("internal = ?", true).
		Where("uid = ?", check.UID).
		Exec(t.Context())
	r.NoError(err)

	env.admin(t, env.acme, "alice@acme.com")

	dark := transition(env.sweep(t), darkRegion)
	r.NotNil(dark, "the region is still dark")
	r.Empty(dark.OrgsNotified)
	r.Empty(env.emails(t, "alice@acme.com"))
}

// TestMigratedRegionRecoversWithoutWorkers: once an operator migrates every
// job away, nothing is stranded: the outage closes (after two sweeps) with a
// "moved" recovery rather than lasting forever.
func TestMigratedRegionRecoversWithoutWorkers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.strand(t)
	env.check(t, env.acme, "Pinned API", darkRegion)
	env.admin(t, env.acme, "alice@acme.com")

	env.sweep(t)

	_, err := env.db.DB().NewDelete().
		Model((*models.CheckJob)(nil)).
		Where("region = ?", darkRegion).
		Exec(t.Context())
	r.NoError(err)

	r.Empty(env.sweep(t).Transitions)

	recovered := transition(env.sweep(t), darkRegion)
	r.NotNil(recovered)
	r.Equal(regionsweep.TransitionRecovered, recovered.Kind)
	r.Len(env.emails(t, "alice@acme.com", email.TemplateRegionRecovered, `"Moved":true`), 1)
}
