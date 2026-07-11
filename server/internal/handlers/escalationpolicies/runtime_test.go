package escalationpolicies_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

func setupRuntime(t *testing.T) (db.Service, jobsvc.Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	org := models.NewOrganization("esc-runtime", "Escalation Runtime Test")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	return dbSvc, jobs, org
}

// TestScheduleEscalationCycleSchedulesAtCumulativeDelays verifies the
// core scheduler primitive: cycle 0 of a 3-step policy schedules three
// jobs at startedAt + d1, + d1+d2, + d1+d2+d3.
func TestScheduleEscalationCycleSchedulesAtCumulativeDelays(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	dbSvc, jobs, org := setupRuntime(t)

	policy := models.NewEscalationPolicy(org.UID, "test")
	r.NoError(dbSvc.CreateEscalationPolicy(ctx, policy))

	steps := []*models.EscalationPolicyStep{
		models.NewEscalationPolicyStep(policy.UID, 0, 0),
		models.NewEscalationPolicyStep(policy.UID, 1, 5),
		models.NewEscalationPolicyStep(policy.UID, 2, 10),
	}
	r.NoError(dbSvc.ReplaceEscalationPolicySteps(ctx, policy.UID, steps, nil))

	startedAt := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	incident := &models.Incident{
		UID:             "incident-1",
		OrganizationUID: org.UID,
		CheckUID:        "check-1",
		StartedAt:       startedAt,
	}

	persisted, err := dbSvc.ListEscalationPolicySteps(ctx, policy.UID)
	r.NoError(err)

	r.NoError(jobtypes.ScheduleEscalationCycle(ctx, jobs, incident, policy, persisted, startedAt, 0, slog.Default()))

	scheduled, err := listEscalationJobs(ctx, dbSvc.DB(), org.UID)
	r.NoError(err)
	r.Len(scheduled, 3)

	// Each job's scheduled_at should match the cumulative delay.
	expected := []time.Time{
		startedAt,
		startedAt.Add(5 * time.Second),
		startedAt.Add(15 * time.Second),
	}
	gotAt := make([]time.Time, len(scheduled))
	for i, job := range scheduled {
		gotAt[i] = job.ScheduledAt
	}

	// Order isn't guaranteed by ListJobs, so sort by scheduled_at.
	sortJobsByTime(scheduled)
	for i, job := range scheduled {
		r.WithinDuration(expected[i], job.ScheduledAt, time.Second)
		cfg := unmarshalStepConfig(t, job.Config)
		r.Equal(incident.UID, cfg.IncidentUID)
		r.Equal(0, cfg.RepeatIndex)
	}

	// The last step must be flagged as such — drives the next-cycle
	// reschedule when repeat_max > 0.
	last := scheduled[len(scheduled)-1]
	lastCfg := unmarshalStepConfig(t, last.Config)
	r.True(lastCfg.IsLastStep, "last step must be flagged IsLastStep")
}

func unmarshalStepConfig(t *testing.T, raw models.JSONMap) jobtypes.EscalationStepJobConfig {
	t.Helper()

	bytes, err := json.Marshal(raw)
	require.NoError(t, err)

	var cfg jobtypes.EscalationStepJobConfig
	require.NoError(t, json.Unmarshal(bytes, &cfg))

	return cfg
}

// TestCancelPendingForIncidentDropsEscalationSteps verifies that the
// existing cancel sweep keyed on `incidentUid` in the job config also
// catches escalation_step jobs without any extra wiring — the headline
// invariant that makes ack/snooze/resolve cancellation Just Work.
func TestCancelPendingForIncidentDropsEscalationSteps(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	dbSvc, jobs, org := setupRuntime(t)

	policy := models.NewEscalationPolicy(org.UID, "cancel test")
	r.NoError(dbSvc.CreateEscalationPolicy(ctx, policy))

	steps := []*models.EscalationPolicyStep{
		models.NewEscalationPolicyStep(policy.UID, 0, 0),
		models.NewEscalationPolicyStep(policy.UID, 1, 5),
	}
	r.NoError(dbSvc.ReplaceEscalationPolicySteps(ctx, policy.UID, steps, nil))

	startedAt := time.Now()
	incident := &models.Incident{
		UID:             "incident-cancel",
		OrganizationUID: org.UID,
		CheckUID:        "check-1",
		StartedAt:       startedAt,
	}

	r.NoError(jobtypes.ScheduleEscalationCycle(ctx, jobs, incident, policy, steps, startedAt, 0, slog.Default()))

	before, err := listEscalationJobs(ctx, dbSvc.DB(), org.UID)
	r.NoError(err)
	r.Len(before, 2)

	// Same call site ack/snooze/resolve uses today.
	canceled, err := jobs.CancelPendingForIncident(ctx, incident.UID, nil)
	r.NoError(err)
	r.Equal(int64(2), canceled, "cancel sweep should match by incidentUid in config")

	after, err := listEscalationJobs(ctx, dbSvc.DB(), org.UID)
	r.NoError(err)
	r.Empty(after, "all escalation step jobs must be canceled after the sweep")
}

func listEscalationJobs(ctx context.Context, dbBun *bun.DB, orgUID string) ([]*models.Job, error) {
	var jobs []*models.Job

	err := dbBun.NewSelect().
		Model(&jobs).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", string(jobdef.JobTypeEscalationStep)).
		Where("deleted_at IS NULL").
		Order("scheduled_at ASC").
		Scan(ctx)

	return jobs, err
}

func sortJobsByTime(jobs []*models.Job) {
	for i := 1; i < len(jobs); i++ {
		for j := i; j > 0 && jobs[j-1].ScheduledAt.After(jobs[j].ScheduledAt); j-- {
			jobs[j-1], jobs[j] = jobs[j], jobs[j-1]
		}
	}
}
