package incidents

// Incident-open integration test (spec 2026-07-19-01): a check that resolves to
// no policy of its own, in an org with a default, schedules cycle 0 from that
// default; a zero-step default schedules nothing.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

func countEscalationJobs(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID string) int {
	t.Helper()

	jobs, err := dbSvc.ListJobs(ctx, &orgUID, 100)
	require.NoError(t, err)

	n := 0
	for _, j := range jobs {
		if j.Type == "escalation_step" {
			n++
		}
	}

	return n
}

// createPolicyWithStep inserts a policy carrying one all-admins step so its
// cycle-0 scheduling produces exactly one escalation_step job.
func createPolicyWithStep(
	ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, name string,
) *models.EscalationPolicy {
	t.Helper()

	policy := models.NewEscalationPolicy(orgUID, name)
	require.NoError(t, dbSvc.CreateEscalationPolicy(ctx, policy))

	step := models.NewEscalationPolicyStep(policy.UID, 0, 0)
	target := &models.EscalationPolicyTarget{
		UID:        step.UID + "-tgt",
		StepUID:    step.UID,
		TargetType: models.EscalationTargetAllAdmins,
		Position:   0,
	}
	require.NoError(t, dbSvc.ReplaceEscalationPolicySteps(
		ctx, policy.UID,
		[]*models.EscalationPolicyStep{step},
		map[int][]*models.EscalationPolicyTarget{0: {target}},
	))

	return policy
}

// TestScheduleEscalationPolicy_OrgDefault walks a single check through three
// org-default states in sequence (the scenarios share one DB and mutate the
// org default, so they run sequentially rather than as parallel subtests).
func TestScheduleEscalationPolicy_OrgDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	require.NoError(t, dbSvc.Initialize(ctx))

	org := models.NewOrganization("sched-esc", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	// A check with no direct policy and no group → resolves to the org default.
	check := models.NewCheck(org.UID, "target-check", "http")
	check.Enabled = false
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	jobsSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := NewService(dbSvc, jobsSvc, clock.Real{}, nil)

	newIncident := func() *models.Incident {
		return models.NewIncident(org.UID, check.UID, time.Now(), "down")
	}

	// 1. No org default → nothing is scheduled (today's behavior, unchanged).
	svc.scheduleEscalationPolicy(ctx, org.UID, check.UID, newIncident())
	require.Equal(t, 0, countEscalationJobs(ctx, t, dbSvc, org.UID),
		"with no org default and no check/group policy, nothing is scheduled")

	// 2. A paging org default schedules its single step at incident open.
	paging := createPolicyWithStep(ctx, t, dbSvc, org.UID, "org-paging")
	require.NoError(t, dbSvc.UpdateOrganization(ctx, org.UID, models.OrganizationUpdate{
		DefaultEscalationPolicyUID: &paging.UID,
	}))
	svc.scheduleEscalationPolicy(ctx, org.UID, check.UID, newIncident())
	require.Equal(t, 1, countEscalationJobs(ctx, t, dbSvc, org.UID),
		"a paging org default must schedule its (single) step at incident open")

	// 3. A zero-step (silent) org default pages nobody — count stays put.
	silent := models.NewEscalationPolicy(org.UID, "org-silent") // no steps
	require.NoError(t, dbSvc.CreateEscalationPolicy(ctx, silent))
	require.NoError(t, dbSvc.UpdateOrganization(ctx, org.UID, models.OrganizationUpdate{
		DefaultEscalationPolicyUID: &silent.UID,
	}))
	svc.scheduleEscalationPolicy(ctx, org.UID, check.UID, newIncident())
	require.Equal(t, 1, countEscalationJobs(ctx, t, dbSvc, org.UID),
		"a zero-step (silent) org default pages nobody (no new job)")
}
