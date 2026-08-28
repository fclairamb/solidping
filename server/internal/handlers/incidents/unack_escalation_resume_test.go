package incidents_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// discardLogger keeps the scheduling helper's own logging out of test output.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// escalationStepJob pairs a step job row with its decoded config.
type escalationStepJob struct {
	job    *models.Job
	config jobtypes.EscalationStepJobConfig
}

// liveEscalationSteps reads the LIVE (not soft-deleted) pending escalation
// step jobs for an org, oldest scheduled_at first.
func liveEscalationSteps(t *testing.T, s *resolveSetup, orgUID string) []escalationStepJob {
	t.Helper()

	var jobs []*models.Job

	err := s.dbSvc.DB().NewSelect().
		Model(&jobs).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", string(jobdef.JobTypeEscalationStep)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Order("scheduled_at ASC").
		Scan(t.Context())
	require.NoError(t, err)

	out := make([]escalationStepJob, 0, len(jobs))

	for _, job := range jobs {
		raw, marshalErr := json.Marshal(job.Config)
		require.NoError(t, marshalErr)

		var cfg jobtypes.EscalationStepJobConfig

		require.NoError(t, json.Unmarshal(raw, &cfg))

		out = append(out, escalationStepJob{job: job, config: cfg})
	}

	return out
}

// threeStepPolicy builds a policy whose steps fire at +1m, +6m and +16m, and
// attaches it to the setup's check.
func threeStepPolicy(t *testing.T, s *resolveSetup) *models.EscalationPolicy {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	policy := models.NewEscalationPolicy(s.org.UID, "pager")
	r.NoError(s.dbSvc.CreateEscalationPolicy(ctx, policy))

	steps := make([]*models.EscalationPolicyStep, 0, 3)
	targets := map[int][]*models.EscalationPolicyTarget{}

	for i, delay := range []int{60, 300, 600} {
		step := models.NewEscalationPolicyStep(policy.UID, i, delay)
		steps = append(steps, step)
		targets[i] = []*models.EscalationPolicyTarget{{
			UID:        step.UID + "-tgt",
			StepUID:    step.UID,
			TargetType: models.EscalationTargetAllAdmins,
			Position:   0,
		}}
	}

	r.NoError(s.dbSvc.ReplaceEscalationPolicySteps(ctx, policy.UID, steps, targets))

	r.NoError(s.dbSvc.UpdateCheck(ctx, s.check.UID, &models.CheckUpdate{
		EscalationPolicyUID: &policy.UID,
	}))

	return policy
}

// The core of the resolved decision (2026-08-28, option (c)): unack must
// reschedule escalation, and it must resume from the step the acknowledgment
// interrupted rather than restarting at step 1.
//
// Setup mirrors reality: cycle 0 is scheduled, step 1 FIRES (its job reaches a
// terminal status), then the ack cancels what is left. The unack must bring
// back steps 2 and 3 — and must not resurrect step 1, which already paged
// somebody.
func TestUnackResumesEscalationFromTheInterruptedStep(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	policy := threeStepPolicy(t, s)

	// Schedule cycle 0 the way incident-open does.
	steps, err := s.dbSvc.ListEscalationPolicySteps(ctx, policy.UID)
	r.NoError(err)
	r.Len(steps, 3)

	r.NoError(jobtypes.ScheduleEscalationCycle(
		ctx, s.jobs, s.incident, policy, steps, s.incident.StartedAt, 0, discardLogger(),
	))

	scheduled := liveEscalationSteps(t, s, s.org.UID)
	r.Len(scheduled, 3)

	firstStepUID := scheduled[0].config.StepUID
	r.Equal(steps[0].UID, firstStepUID)
	r.True(scheduled[2].config.IsLastStep, "the last rung carries the repeat-cycle flag")

	// Step 1 fires: its job leaves 'pending', which is exactly what stops the
	// resume from replaying it.
	_, err = s.dbSvc.DB().NewUpdate().
		Model((*models.Job)(nil)).
		Set("status = ?", string(models.JobStatusSuccess)).
		Where("uid = ?", scheduled[0].job.UID).
		Exec(ctx)
	r.NoError(err)

	// The acknowledgment cancels the rest of the cycle.
	_, err = s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	r.Empty(liveEscalationSteps(t, s, s.org.UID),
		"the ack must leave no escalation step pending — otherwise this test proves nothing")

	// The withdrawal.
	_, err = s.svc.UnacknowledgeIncident(ctx, s.org.Slug, s.incident.UID, "", "web")
	r.NoError(err)

	resumed := liveEscalationSteps(t, s, s.org.UID)
	r.Len(resumed, 2, "exactly the two rungs the acknowledgment interrupted come back")

	// THE assertion: the identity of what was rescheduled. A restart at step 1
	// would page everyone the first rung already woke.
	r.Equal(steps[1].UID, resumed[0].config.StepUID,
		"escalation must resume at the step the ack interrupted, not at step 1")
	r.Equal(steps[2].UID, resumed[1].config.StepUID)

	for _, step := range resumed {
		r.NotEqual(firstStepUID, step.config.StepUID,
			"a step that already fired must never be replayed by an unack")
		r.Equal(0, step.config.RepeatIndex,
			"the cycle continues, so the repeat index is the interrupted one, not a fresh cycle")
		r.Equal(policy.UID, step.config.PolicyUID)
	}

	r.True(resumed[1].config.IsLastStep,
		"the repeat-cycle flag rides along untouched, so repeats continue from this cycle")

	// The relative spacing between the surviving rungs is preserved (the third
	// rung's own delay_seconds is 600), so a resume is not a burst of pages.
	gap := resumed[1].job.ScheduledAt.Sub(resumed[0].job.ScheduledAt)
	r.InDelta(float64(10*time.Minute), float64(gap), float64(2*time.Second),
		"resuming keeps the policy's own spacing between the remaining rungs")
}

// A second cycle is the case that would silently regress into "restart from
// the beginning": if the ack interrupts repeat cycle 2, the resume must come
// back on cycle 2, not on cycle 0.
func TestUnackResumesTheInterruptedRepeatCycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	policy := threeStepPolicy(t, s)

	steps, err := s.dbSvc.ListEscalationPolicySteps(ctx, policy.UID)
	r.NoError(err)

	// Cycle 2 is in flight when the acknowledgment lands.
	r.NoError(jobtypes.ScheduleEscalationCycle(
		ctx, s.jobs, s.incident, policy, steps, time.Now(), 2, discardLogger(),
	))

	_, err = s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	_, err = s.svc.UnacknowledgeIncident(ctx, s.org.Slug, s.incident.UID, "", "web")
	r.NoError(err)

	resumed := liveEscalationSteps(t, s, s.org.UID)
	r.Len(resumed, 3)

	for _, step := range resumed {
		r.Equal(2, step.config.RepeatIndex,
			"the resumed cycle is the one the ack interrupted, not cycle 0")
	}
}

// Unack must never START an escalation that was not running: an incident whose
// check has no policy (or whose cycle had already run itself out) gets a chat
// notice and nothing else.
func TestUnackDoesNotStartEscalationThatWasNotRunning(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	_, err = s.svc.UnacknowledgeIncident(ctx, s.org.Slug, s.incident.UID, "", "web")
	r.NoError(err)

	r.Empty(liveEscalationSteps(t, s, s.org.UID),
		"no policy was paging, so unack has nothing to resume")

	// Positive control: the retraction itself still went out, so the empty
	// result above is about escalation and not about a dead code path.
	r.NotEmpty(pendingUnackNoticeJobs(t, s, s.org.UID))
}

// An ack → unack → ack → unack sequence leaves two canceled generations of the
// same rung behind. Resuming both would page the same person twice per rung.
func TestUnackDoesNotDuplicateRungsAcrossRepeatedAckCycles(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	policy := threeStepPolicy(t, s)

	steps, err := s.dbSvc.ListEscalationPolicySteps(ctx, policy.UID)
	r.NoError(err)

	r.NoError(jobtypes.ScheduleEscalationCycle(
		ctx, s.jobs, s.incident, policy, steps, time.Now(), 0, discardLogger(),
	))

	for range 2 {
		_, ackErr := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
			IncidentUID: s.incident.UID,
			Via:         "web",
		})
		r.NoError(ackErr)

		_, unackErr := s.svc.UnacknowledgeIncident(ctx, s.org.Slug, s.incident.UID, "", "web")
		r.NoError(unackErr)
	}

	resumed := liveEscalationSteps(t, s, s.org.UID)
	r.Len(resumed, 3, "two canceled generations must collapse back to one live cycle")

	seen := map[string]bool{}
	for _, step := range resumed {
		key := step.config.StepUID
		r.False(seen[key], "a rung must be scheduled at most once after a resume")
		seen[key] = true
	}
}
