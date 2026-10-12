package jobtypes

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
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

// resolveRetentionDays returns how many days of audit history to keep, or 0
// when every event must be kept.
//
// Precedence: SP_AUDIT_RETENTION_DAYS, then the global audit.retention_days
// DB parameter, then the koanf value, then the 365-day default. At every tier a
// value <= 0 means "keep forever": an explicit, supported choice for an operator
// under a legal hold, so it is returned as 0 rather than falling through. Only
// an absent or unparseable value falls through to the next tier. The koanf tier
// is deliberate when AppConfig is set, because config.Load already defaults it
// to 365.
//
// This does not use resolveRetentionTier: there 0 is invalid (the performance
// tiers need >= 1).
func (r *EventsCleanupJobRun) resolveRetentionDays(ctx context.Context, jctx *jobdef.JobContext) int {
	return resolveAuditRetentionDays(ctx, jctx, os.LookupEnv)
}

func resolveAuditRetentionDays(
	ctx context.Context, jctx *jobdef.JobContext, lookupEnv func(string) (string, bool),
) int {
	log := aggregationLogger(jctx)

	if raw, ok := lookupEnv("SP_AUDIT_RETENTION_DAYS"); ok && strings.TrimSpace(raw) != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			return keepForeverAsZero(n)
		}

		log.WarnContext(ctx, "Ignoring unparseable SP_AUDIT_RETENTION_DAYS, falling through", "value", raw)
	}

	if jctx != nil && jctx.DBService != nil {
		param, err := jctx.DBService.GetSystemParameter(ctx, string(systemconfig.KeyAuditRetentionDays))
		if err == nil && param != nil {
			if n, ok := parameterInt(param); ok {
				return keepForeverAsZero(n)
			}

			log.WarnContext(ctx, "Ignoring unparseable audit.retention_days parameter, falling through")
		}
	}

	if jctx != nil && jctx.AppConfig != nil {
		return keepForeverAsZero(jctx.AppConfig.Audit.RetentionDays)
	}

	return config.DefaultAuditRetentionDays
}

// keepForeverAsZero normalizes any non-positive retention to 0 (keep forever).
func keepForeverAsZero(n int) int {
	if n <= 0 {
		return 0
	}

	return n
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

	log.DebugContext(ctx, "Starting audit events cleanup",
		"retentionDays", retentionDays, "before", before)

	deleted, err := r.deletePass(ctx, jctx, before)
	if err != nil {
		log.ErrorContext(ctx, "Failed to delete expired audit events", "error", err, "deleted", deleted)

		return jobdef.NewRetryableError(err)
	}

	log.Log(ctx, levelForCount(deleted), "Deleted expired audit events", "count", deleted)

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
