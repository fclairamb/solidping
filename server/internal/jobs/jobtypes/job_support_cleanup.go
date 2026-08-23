package jobtypes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// support_cleanup enforces retention on captured support conversations
// (spec 2026-08-22-02).
//
// This is not housekeeping. Support message bodies are PERSONAL DATA — free
// text written by identifiable people, arriving from publicly reachable phone
// numbers — and the feature introduces a category of stored personal data the
// product did not have before. Shipping the capture without a retention period
// would put the published privacy policy in conflict with what the service
// actually does, so the sweep lands in the same change as the capture.
//
// Only CLOSED threads are purged, and only once they have been closed longer
// than the retention window. An open conversation is live business.
const (
	// supportCleanupBatchSize bounds one DELETE pass.
	supportCleanupBatchSize = 500
	// supportCleanupMaxBatches bounds one RUN, so a first sweep over a long
	// backlog cannot monopolize a job worker.
	supportCleanupMaxBatches = 20
	// supportCleanupInterval is the self-reschedule delay.
	supportCleanupInterval = 24 * time.Hour
	// DefaultSupportRetentionDays is the shipped retention period for closed
	// support threads: twelve months.
	DefaultSupportRetentionDays = 365
)

// SupportCleanupJobDefinition is the factory for the support-retention sweeper.
type SupportCleanupJobDefinition struct{}

// Type returns the support cleanup job type.
func (d *SupportCleanupJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeSupportCleanup
}

// SupportCleanupJobConfig is the empty config for the sweeper.
type SupportCleanupJobConfig struct{}

// CreateJobRun builds an executable instance.
func (d *SupportCleanupJobDefinition) CreateJobRun(configRaw json.RawMessage) (jobdef.JobRunner, error) {
	var cfg SupportCleanupJobConfig
	if len(configRaw) > 0 {
		if err := json.Unmarshal(configRaw, &cfg); err != nil {
			return nil, err
		}
	}

	return &SupportCleanupJobRun{}, nil
}

// SupportCleanupJobRun is the runtime state for one sweep.
type SupportCleanupJobRun struct {
	// batchSize overrides supportCleanupBatchSize when > 0 (tests only).
	batchSize int
	// maxBatches overrides supportCleanupMaxBatches when > 0 (tests only).
	maxBatches int
}

func (r *SupportCleanupJobRun) batch() int {
	if r.batchSize > 0 {
		return r.batchSize
	}

	return supportCleanupBatchSize
}

func (r *SupportCleanupJobRun) batchLimit() int {
	if r.maxBatches > 0 {
		return r.maxBatches
	}

	return supportCleanupMaxBatches
}

// Run purges closed support threads older than the retention window.
func (r *SupportCleanupJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.Services == nil || jctx.Services.Support == nil {
		log.InfoContext(ctx, "Support inbox not configured; skipping retention sweep")
		r.rescheduleSelf(ctx, jctx)

		return nil
	}

	// <= 0 means "keep forever" and is a supported, deliberate choice — an
	// operator under a legal hold must be able to switch the sweep off without
	// removing the job.
	retentionDays := resolveRetentionTier(ctx, jctx,
		systemconfig.KeySupportRetentionDays, "SP_SUPPORT_RETENTION_DAYS",
		0, DefaultSupportRetentionDays)
	if retentionDays <= 0 {
		log.InfoContext(ctx, "Support retention disabled; keeping every thread")
		r.rescheduleSelf(ctx, jctx)

		return nil
	}

	before := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)

	var total int64

	for i := 0; i < r.batchLimit(); i++ {
		purged, err := jctx.Services.Support.PurgeClosedBefore(ctx, before, r.batch())
		if err != nil {
			log.ErrorContext(ctx, "Failed to purge closed support threads",
				"error", err, "purged", total)

			return jobdef.NewRetryableError(err)
		}

		total += purged

		if purged < int64(r.batch()) {
			break
		}
	}

	log.InfoContext(ctx, "Purged expired support threads",
		"count", total, "retentionDays", retentionDays)

	r.rescheduleSelf(ctx, jctx)

	return nil
}

func (r *SupportCleanupJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(supportCleanupInterval)

	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeSupportCleanup), nil, &jobsvc.JobOptions{
		ScheduledAt: &scheduledAt,
	})
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule support cleanup", "error", err)
	}
}
