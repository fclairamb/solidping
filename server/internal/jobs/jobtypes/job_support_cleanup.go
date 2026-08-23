package jobtypes

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
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

	retentionDays := resolveSupportRetentionDays(ctx, jctx)
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

// resolveSupportRetentionDays resolves the retention window through the
// documented precedence: env var → global DB parameter → hardcoded default.
//
// It does NOT reuse resolveRetentionTier, which rejects values below 1 and
// falls through to the default. Here ZERO IS MEANINGFUL: it means "keep
// forever", the switch an operator under a legal hold needs. Silently turning
// that into 365 days would delete the very records they are obliged to keep.
func resolveSupportRetentionDays(ctx context.Context, jctx *jobdef.JobContext) int {
	log := jctx.Logger

	if raw := strings.TrimSpace(os.Getenv("SP_SUPPORT_RETENTION_DAYS")); raw != "" {
		if days, err := strconv.Atoi(raw); err == nil && days >= 0 {
			return days
		}

		log.WarnContext(ctx, "Ignoring invalid SP_SUPPORT_RETENTION_DAYS", "value", raw)
	}

	if jctx.DBService != nil {
		param, err := jctx.DBService.GetSystemParameter(ctx, string(systemconfig.KeySupportRetentionDays))
		if err == nil && param != nil {
			if days, ok := supportRetentionFromParam(param.Value); ok {
				return days
			}

			log.WarnContext(ctx, "Ignoring invalid support.retention_days parameter")
		}
	}

	return DefaultSupportRetentionDays
}

// supportRetentionFromParam reads the retention value out of a system parameter
// row, tolerating both the float64 a JSON round trip produces and a plain int.
func supportRetentionFromParam(value models.JSONMap) (int, bool) {
	raw, ok := value["value"]
	if !ok {
		return 0, false
	}

	switch typed := raw.(type) {
	case float64:
		if typed >= 0 {
			return int(typed), true
		}
	case int:
		if typed >= 0 {
			return typed, true
		}
	case int64:
		if typed >= 0 {
			return int(typed), true
		}
	}

	return 0, false
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
