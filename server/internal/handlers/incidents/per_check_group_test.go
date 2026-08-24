package incidents_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// The RabbitMQ outage of 2026-08-23 in test form (spec 2026-08-24-14).
//
// Group incidents keyed the whole group to whichever member failed FIRST.
// When the core prod check went down 25 minutes later it joined that stale
// incident as a member row instead of opening its own, and three things broke
// at once: its check page showed no incident, it inherited an escalation that
// had already fired for a different cluster, and — worst — it could never be
// found as a dependency-rollup parent, because rollup matches parents on
// incidents.check_uid. 55 dependent incidents paged individually for one root
// cause.
//
// These tests pin the fix from the outside: a check in a group is now an
// ordinary check as far as incidents are concerned.

type groupIncidentSetup struct {
	svc   *incidents.Service
	dbSvc *sqlite.Service
	clk   *clock.Fake
	org   *models.Organization
	group *models.CheckGroup
}

func newGroupIncidentSetup(t *testing.T) *groupIncidentSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	clk := clock.NewFake(time.Date(2026, 8, 23, 23, 20, 0, 0, time.UTC))
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clk, nil)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	group := models.NewCheckGroup(org.UID, "RabbitMQ", "rabbitmq")
	r.NoError(dbSvc.CreateCheckGroup(ctx, group))

	return &groupIncidentSetup{svc: svc, dbSvc: dbSvc, clk: clk, org: org, group: group}
}

// member creates an enabled check inside the group, with its own notification
// channel bound so "did this incident page anyone" is answerable.
func (s *groupIncidentSetup) member(t *testing.T, slug string) *models.Check {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	check := models.NewCheck(s.org.UID, slug, "tcp")
	check.CheckGroupUID = &s.group.UID
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 0
	check.RecoveryPeriodSeconds = 0
	r.NoError(s.dbSvc.CreateCheck(ctx, check))

	conn := models.NewIntegration(s.org.UID, models.ConnectionTypeWebhook, slug+"-ops")
	conn.Enabled = true
	r.NoError(s.dbSvc.CreateChannel(ctx, conn))
	r.NoError(s.dbSvc.CreateCheckConnection(ctx, models.NewCheckConnection(check.UID, conn.UID, s.org.UID)))

	return check
}

// fail pushes one failing result through the real state machine.
func (s *groupIncidentSetup) fail(t *testing.T, check *models.Check) *models.Check {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	result := models.NewResult(s.org.UID, check.UID, models.ResultStatusDown, 12)
	result.PeriodStart = s.clk.Now()
	result.Output["error"] = "connection refused"
	r.NoError(s.dbSvc.CreateResult(ctx, result))
	r.NoError(s.svc.ProcessCheckResult(context.Background(), check, result))

	reloaded, err := s.dbSvc.GetCheck(ctx, s.org.UID, check.UID)
	r.NoError(err)

	return reloaded
}

// TestGroupMembersOpenIndependentIncidents is the core behavior change: two
// members of the same group failing 25 minutes apart produce TWO incidents,
// each keyed to its own check, each independently notifiable.
//
// The old model produced one incident owned by the first member, with the
// second reduced to an incident_member_checks row — which is why the second
// check's page showed nothing and why its outage never paged afresh.
func TestGroupMembersOpenIndependentIncidents(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s := newGroupIncidentSetup(t)

	nonprod := s.member(t, "rabbitmq-nonprod")
	prod := s.member(t, "rabbitmq-prod")

	// 23:23 — nonprod times out.
	nonprod = s.fail(t, nonprod)

	nonprodInc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, nonprod.UID)
	r.NoError(err)
	r.Equal(nonprod.UID, nonprodInc.CheckUID)
	r.Nil(nonprodInc.CheckGroupUID, "a grouped check opens a PER-CHECK incident")

	pagedForNonprod := pendingNotificationJobs(t, s.dbSvc, s.org.UID)
	r.Positive(pagedForNonprod, "the first member's outage pages")

	// 23:48 — prod goes down, 25 minutes later. A different cluster, plausibly
	// an unrelated cause; the old code asserted a correlation that never held.
	s.clk.Advance(25 * time.Minute)

	prod = s.fail(t, prod)

	prodInc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, prod.UID)
	r.NoError(err)
	r.Equal(prod.UID, prodInc.CheckUID,
		"the prod check's own incident — not a member row on the nonprod incident")
	r.Nil(prodInc.CheckGroupUID)
	r.NotEqual(nonprodInc.UID, prodInc.UID, "two members failing apart are two incidents")

	// Neither incident absorbed the other as a member.
	for _, inc := range []*models.Incident{nonprodInc, prodInc} {
		members, memErr := s.dbSvc.ListIncidentMemberChecks(ctx, inc.UID)
		r.NoError(memErr)
		r.Empty(members, "incident_member_checks gains no new rows")
	}

	// And prod paged on its own account rather than inheriting an escalation
	// that had already fired for nonprod half an hour earlier.
	r.Greater(pendingNotificationJobs(t, s.dbSvc, s.org.UID), pagedForNonprod,
		"the second member's outage pages independently")
	r.Nil(prodInc.EscalatedAt, "a fresh incident starts with a fresh escalation clock")
}

// TestGroupedCheckCanBeARollupParent is the defect this spec exists to fix,
// stated as narrowly as it happened.
//
// findRollupRoot matches parent incidents on incidents.check_uid. Under group
// incidents, a member that was not the group's FIRST failure had no row with
// its own check_uid, so it could never be found — every one of its dependents
// paged separately for a cause that was already known.
//
// The setup reproduces exactly that ordering: a sibling fails first, THEN the
// parent. If grouped checks still merged, the parent would be a member row and
// the rollup below would find nothing.
func TestGroupedCheckCanBeARollupParent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s := newGroupIncidentSetup(t)

	sibling := s.member(t, "rabbitmq-nonprod")
	parent := s.member(t, "rabbitmq-prod")

	// The sibling fails FIRST — the ordering that used to make the group
	// incident belong to the wrong check.
	sibling = s.fail(t, sibling)

	siblingInc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, sibling.UID)
	r.NoError(err)
	r.NotNil(siblingInc)

	s.clk.Advance(25 * time.Minute)

	parent = s.fail(t, parent)

	parentInc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, parent.UID)
	r.NoError(err)
	r.Equal(parent.UID, parentInc.CheckUID,
		"the parent must own an incident keyed to ITSELF, or rollup can never find it")

	// A dependent of the grouped parent.
	child := models.NewCheck(s.org.UID, "consumer-api", "http")
	r.NoError(s.dbSvc.CreateCheck(ctx, child))
	r.NoError(s.dbSvc.CreateCheckDependency(ctx,
		models.NewCheckDependency(s.org.UID, parent.UID, child.UID, models.CheckDependencyKindHard, nil)))

	childInc := models.NewIncident(s.org.UID, child.UID, s.clk.Now(), "consumer-api is down")
	s.svc.ApplyRollupForTest(ctx, child, childInc)

	r.True(childInc.PagingSuppressed,
		"a dependent of a grouped parent must roll up instead of paging")
	r.NotNil(childInc.CausedByIncidentUID)
	r.Equal(parentInc.UID, *childInc.CausedByIncidentUID,
		"attributed to the PARENT's own incident, not the sibling's")
	r.NotEqual(siblingInc.UID, *childInc.CausedByIncidentUID)
}

// TestGroupedCheckIncidentIsVisibleOnItsCheckPage pins consequence #1 of the
// outage: check pages and `GET /incidents?checkUid=` filter on
// incidents.check_uid, and membership in incident_member_checks is invisible
// there. A grouped check's outage was therefore unfindable from its own page.
func TestGroupedCheckIncidentIsVisibleOnItsCheckPage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s := newGroupIncidentSetup(t)

	first := s.member(t, "rabbitmq-nonprod")
	prod := s.member(t, "rabbitmq-prod")

	first = s.fail(t, first)
	s.clk.Advance(25 * time.Minute)
	prod = s.fail(t, prod)

	resp, err := s.svc.ListIncidents(ctx, s.org.Slug, &incidents.ListIncidentsOptions{
		CheckUIDs: []string{prod.UID},
		Size:      10,
	})
	r.NoError(err)
	r.Len(resp.Data, 1, "the grouped check's own incident must be listed under its checkUid")
	r.Equal(prod.UID, resp.Data[0].CheckUID)

	// And the sibling's incident is NOT dragged in by the shared group.
	r.NotEqual(first.UID, resp.Data[0].CheckUID)
}
