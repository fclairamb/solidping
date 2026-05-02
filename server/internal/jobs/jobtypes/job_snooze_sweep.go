package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// snoozeSweepInterval controls how often the sweeper wakes up. The snooze
// resolution the operator perceives is bounded by this — a 60s lag between
// "snooze expired" and "notifications resume" is fine for an alerting-tier
// SLA.
const snoozeSweepInterval = time.Minute

// SnoozeSweepJobDefinition is the factory for the auto-unsnooze sweeper.
type SnoozeSweepJobDefinition struct{}

// Type returns the snooze sweep job type.
func (d *SnoozeSweepJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeSnoozeSweep
}

// SnoozeSweepJobConfig is the empty config for the sweeper.
type SnoozeSweepJobConfig struct{}

// CreateJobRun builds an executable instance.
func (d *SnoozeSweepJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg SnoozeSweepJobConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}

	return &SnoozeSweepJobRun{}, nil
}

// SnoozeSweepJobRun is the runtime state for one execution.
type SnoozeSweepJobRun struct{}

// Run clears expired snoozes on active incidents and reschedules itself.
// The sweep talks to DBService directly rather than the incidents service to
// avoid an import cycle (incidents.Service depends on jobsvc, which would
// depend on incidents.Service if we routed through the registry).
func (r *SnoozeSweepJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger
	now := time.Now()

	incidents, err := jctx.DBService.ListExpiredSnoozedIncidents(ctx, now)
	if err != nil {
		log.ErrorContext(ctx, "Failed to list expired snoozes", "error", err)

		return jobdef.NewRetryableError(fmt.Errorf("list expired snoozes: %w", err))
	}

	for _, incident := range incidents {
		if err := r.unsnooze(ctx, jctx, incident); err != nil {
			log.WarnContext(ctx, "Failed to auto-unsnooze incident",
				"incident_uid", incident.UID, "error", err)
		}
	}

	if len(incidents) > 0 {
		log.InfoContext(ctx, "Auto-unsnoozed expired incidents", "count", len(incidents))
	}

	r.rescheduleSelf(ctx, jctx)

	return nil
}

func (r *SnoozeSweepJobRun) unsnooze(
	ctx context.Context, jctx *jobdef.JobContext, incident *models.Incident,
) error {
	update := models.IncidentUpdate{
		ClearSnoozedUntil: true,
		ClearSnoozedBy:    true,
		ClearSnoozeReason: true,
	}
	if err := jctx.DBService.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("update incident: %w", err)
	}

	event := models.NewEvent(incident.OrganizationUID, models.EventTypeIncidentUnsnoozed, models.ActorTypeSystem)
	event.IncidentUID = &incident.UID
	event.Payload = models.JSONMap{"via": "auto"}

	if err := jctx.DBService.CreateEvent(ctx, event); err != nil {
		return fmt.Errorf("create unsnooze event: %w", err)
	}

	return nil
}

func (r *SnoozeSweepJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(snoozeSweepInterval)

	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeSnoozeSweep), nil, &jobsvc.JobOptions{
		ScheduledAt: &scheduledAt,
	})
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule snooze sweep", "error", err)
	}
}
