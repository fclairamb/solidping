package jobtypes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// events_cleanup enforces audit retention (spec 2026-08-21-09).
//
// The `events` table had no retention at all before this: it was append-only
// and grew forever, which was survivable while it only held check/incident
// lifecycle rows and stops being survivable once every login, every config
// edit and every failed authentication lands in it. Rather than inventing a
// second mechanism, this follows the jobs_cleanup shape exactly — daily,
// self-rescheduling, batched, window resolved per run through env → global DB
// parameter → koanf → default.
const (
	// eventsCleanupBatchSize bounds each DELETE so a first sweep on a
	// long-lived installation cannot hold one enormous transaction.
	eventsCleanupBatchSize = 10_000
	// eventsCleanupMaxBatches bounds one RUN. A first sweep against years of
	// history should not monopolize a job worker for an hour; whatever is left
	// drains tomorrow, and the day after, until the backlog is gone.
	eventsCleanupMaxBatches = 50
	// eventsCleanupInterval is the self-reschedule delay.
	eventsCleanupInterval = 24 * time.Hour
)

// EventsCleanupJobDefinition is the factory for the audit-retention sweeper.
type EventsCleanupJobDefinition struct{}

// Type returns the events cleanup job type.
func (d *EventsCleanupJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeEventsCleanup
}

// EventsCleanupJobConfig is the empty config for the sweeper.
type EventsCleanupJobConfig struct{}

// CreateJobRun builds an executable instance.
func (d *EventsCleanupJobDefinition) CreateJobRun(configRaw json.RawMessage) (jobdef.JobRunner, error) {
	var cfg EventsCleanupJobConfig
	if len(configRaw) > 0 {
		if err := json.Unmarshal(configRaw, &cfg); err != nil {
			return nil, err
		}
	}

	return &EventsCleanupJobRun{}, nil
}

// EventsCleanupJobRun is the runtime state for one sweep.
type EventsCleanupJobRun struct {
	// batchSize overrides eventsCleanupBatchSize when > 0. Production leaves it
	// zero; tests set a small value to exercise the multi-batch drain loop
	// without seeding tens of thousands of rows.
	batchSize int
	// maxBatches overrides eventsCleanupMaxBatches when > 0.
	maxBatches int
}

func (r *EventsCleanupJobRun) batch() int {
	if r.batchSize > 0 {
		return r.batchSize
	}

	return eventsCleanupBatchSize
}

func (r *EventsCleanupJobRun) batchLimit() int {
	if r.maxBatches > 0 {
		return r.maxBatches
	}

	return eventsCleanupMaxBatches
}

// resolveRetentionDays returns how many days of audit history to keep.
//
// A value <= 0 means "keep forever" and is an explicit, supported choice — an
// operator under a legal hold must be able to turn the sweep off without
// having to remove the job. That is why the koanf legacy tier is passed
// through rather than being normalized to the default first: config.Load
// already defaults it to 365, so a 0 here can only have come from someone
// setting it deliberately.
func (r *EventsCleanupJobRun) resolveRetentionDays(ctx context.Context, jctx *jobdef.JobContext) int {
	legacy := 0
	if jctx != nil && jctx.AppConfig != nil {
		legacy = jctx.AppConfig.Audit.RetentionDays
	}

	if legacy < 0 {
		return 0
	}

	return resolveRetentionTier(ctx, jctx,
		systemconfig.KeyAuditRetentionDays, "SP_AUDIT_RETENTION_DAYS",
		legacy, config.DefaultAuditRetentionDays)
}

// Run deletes audit events older than the retention window, then reschedules
// itself one interval out.
func (r *EventsCleanupJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	retentionDays := r.resolveRetentionDays(ctx, jctx)
	if retentionDays <= 0 {
		log.InfoContext(ctx, "Audit retention disabled; keeping every event")
		r.rescheduleSelf(ctx, jctx)

		return nil
	}

	before := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)

	log.InfoContext(ctx, "Starting audit events cleanup",
		"retentionDays", retentionDays, "before", before)

	deleted, err := r.deletePass(ctx, jctx, before)
	if err != nil {
		log.ErrorContext(ctx, "Failed to delete expired audit events", "error", err, "deleted", deleted)

		return jobdef.NewRetryableError(err)
	}

	log.InfoContext(ctx, "Deleted expired audit events", "count", deleted)

	r.rescheduleSelf(ctx, jctx)

	return nil
}

// deletePass deletes in batches until a short batch drains the backlog or the
// per-run batch ceiling is hit.
func (r *EventsCleanupJobRun) deletePass(
	ctx context.Context, jctx *jobdef.JobContext, before time.Time,
) (int64, error) {
	var total int64

	for i := 0; i < r.batchLimit(); i++ {
		n, err := jctx.DBService.DeleteEventsBefore(ctx, before, r.batch())
		if err != nil {
			return total, err
		}

		total += n

		if n < int64(r.batch()) {
			break
		}
	}

	return total, nil
}

func (r *EventsCleanupJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(eventsCleanupInterval)

	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeEventsCleanup), nil, &jobsvc.JobOptions{
		ScheduledAt: &scheduledAt,
	})
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule events cleanup", "error", err)
	}
}
