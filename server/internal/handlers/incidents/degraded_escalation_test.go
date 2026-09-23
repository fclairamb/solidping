package incidents

// Degraded incidents notify but never PAGE (spec 2026-09-22-03, resolved open
// question 2). This is the test that pins it, because the difference is invisible
// in the incident row: the wording requirement ("must not resemble an outage or
// people mute both") is undone the moment the same incident wakes on-call through
// the check's escalation policy, and nothing else in the product would notice.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// TestDegradedIncidentSchedulesNoEscalation opens one degraded and one check
// incident on the SAME check, under the same paging policy, and asserts only the
// outage produced an escalation_step job. The check incident is the positive
// control: without it a broken fixture (no policy resolved at all) would make the
// degraded half pass for the wrong reason.
func TestDegradedIncidentSchedulesNoEscalation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("degraded-esc", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	policy := createPolicyWithStep(ctx, t, dbSvc, org.UID, "pager")

	check := models.NewCheck(org.UID, "acme", "http")
	check.Enabled = false
	check.EscalationPolicyUID = &policy.UID
	r.NoError(dbSvc.CreateCheck(ctx, check))

	jobsSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := NewService(dbSvc, jobsSvc, clock.Real{}, nil)

	pct := 88.3
	_, err = svc.OpenDegradedIncident(ctx, &OpenDegradedIncidentRequest{
		Check:     check,
		StartedAt: time.Now().Add(-30 * time.Minute),
		Title:     "acme is degraded: 7 failures in the last 60 probes (88.3%)",
		Snapshot: &DegradedSnapshot{
			Failures: 5, FailuresWindow: 60, FailureSlots: 60, FailureMatches: 7,
			FailuresFired: true, AvailabilityPct: &pct, ResolveWindow: 60, CurrentlyUp: true,
		},
	})
	r.NoError(err)

	r.Equal(0, countEscalationJobs(ctx, t, dbSvc, org.UID),
		"a degraded incident must schedule no escalation cycle — notify-only, no paging")

	// Positive control: the very same policy on the very same check DOES page for
	// a real outage.
	svc.scheduleEscalationPolicy(ctx, org.UID, check.UID,
		models.NewIncident(org.UID, check.UID, time.Now(), "acme is down"))

	r.Equal(1, countEscalationJobs(ctx, t, dbSvc, org.UID),
		"the fixture's policy really does page — so the zero above is about the kind")
}

// TestDegradedIncidentIsNeverCascadeParent pins resolved open question 1: a
// degraded incident must not participate in dependency-cascade suppression at
// all, so a descendant's real outage can never be rolled up under (and silenced
// by) an ancestor that is merely intermittent.
func TestDegradedIncidentIsNeverCascadeParent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("degraded-cascade", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	parent := models.NewCheck(org.UID, "gateway", "http")
	parent.Enabled = false
	r.NoError(dbSvc.CreateCheck(ctx, parent))

	// A degraded incident on the would-be ancestor, open right now.
	degradedIncident := models.NewIncident(org.UID, parent.UID, time.Now().Add(-10*time.Minute), "gateway is degraded")
	degradedIncident.Kind = models.IncidentKindDegraded
	r.NoError(dbSvc.CreateIncident(ctx, degradedIncident))

	// The cascade's own lookup is the single gate every rollup decision passes
	// through. It must not see the degraded row.
	found, err := dbSvc.FindActiveIncidentsForChecksInWindow(
		ctx, []string{parent.UID}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour),
	)
	r.NoError(err)
	r.Empty(found, "a degraded incident must never be offered as a cascade parent")

	// Positive control: a real outage on the same check in the same window IS.
	outage := models.NewIncident(org.UID, parent.UID, time.Now().Add(-5*time.Minute), "gateway is down")
	r.NoError(dbSvc.CreateIncident(ctx, outage))

	found, err = dbSvc.FindActiveIncidentsForChecksInWindow(
		ctx, []string{parent.UID}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour),
	)
	r.NoError(err)
	r.Len(found, 1, "the lookup does work — the exclusion above is about the kind")
	r.Equal(outage.UID, found[0].UID)
}
