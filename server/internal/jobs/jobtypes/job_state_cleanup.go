package jobtypes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// StateCleanupJobDefinition is the factory for state cleanup jobs.
type StateCleanupJobDefinition struct{}

// Type returns the job type for state cleanup jobs.
func (d *StateCleanupJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeStateCleanup
}

// StateCleanupJobConfig is the configuration for a state cleanup job.
// Empty - no configuration needed.
type StateCleanupJobConfig struct{}

// CreateJobRun creates a new state cleanup job run from the given configuration.
func (d *StateCleanupJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg StateCleanupJobConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}

	return &StateCleanupJobRun{config: cfg}, nil
}

// StateCleanupJobRun is an executable state cleanup job instance.
type StateCleanupJobRun struct {
	config StateCleanupJobConfig
}

// Run executes the state cleanup job.
func (r *StateCleanupJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	log.InfoContext(ctx, "Starting state cleanup job")

	// Delete expired state entries
	count, err := jctx.DBService.DeleteExpiredStateEntries(ctx)
	if err != nil {
		log.ErrorContext(ctx, "Failed to delete expired state entries", "error", err)
		return jobdef.NewRetryableError(err)
	}

	if count > 0 {
		log.InfoContext(ctx, "Deleted expired state entries", "count", count)
	} else {
		log.InfoContext(ctx, "No expired state entries to delete")
	}

	sweepOrphanIncidentAttachments(ctx, jctx)

	// Schedule next run in 2 hours
	// Skip if services are not available (e.g., in tests without full service setup)
	if jctx.Services != nil && jctx.Services.Jobs != nil {
		delay := 2 * time.Hour
		scheduledAt := time.Now().Add(delay)
		_, err = jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeStateCleanup), nil, &jobsvc.JobOptions{
			ScheduledAt: &scheduledAt,
		})
		if err != nil {
			log.ErrorContext(ctx, "Failed to schedule next state cleanup job", "error", err)
		}
	}

	return nil
}

// attachmentOrphanGrace is how long an attachment must have existed before the
// sweep will consider it. It is a RACE GUARD, not a retention policy: an
// attachment row is written moments after its incident, and on the agent path
// the upload can even arrive before the caller has finished with the incident,
// so sweeping a fresh row risks deleting evidence for an entity that exists
// perfectly well.
const attachmentOrphanGrace = 6 * time.Hour

// attachmentSweepBatch bounds one pass. The sweep runs every two hours off the
// back of the state-cleanup job; there is no value in doing the whole backlog
// in one transaction-hostile burst.
const attachmentSweepBatch = 500

// sweepOrphanIncidentAttachments soft-deletes attachment rows whose
// `incidents/<uid>/…` topic points at an incident that no longer exists.
//
// WHY THIS IS NOT OPTIONAL: an attachment is a blob on somebody's storage
// bill. Without a reaper, every deleted incident leaves its screenshot behind
// forever, and the cost of the feature grows monotonically with churn rather
// than with the number of incidents an operator can actually look at. The
// topic IS the link, so a topic pointing at nothing is the exact definition of
// an orphan — no extra bookkeeping table is needed to find one.
//
// Best-effort and never fatal: this is housekeeping riding on a cleanup job,
// and a failure here must not stop expired state entries from being reaped or
// the next run from being scheduled.
func sweepOrphanIncidentAttachments(ctx context.Context, jctx *jobdef.JobContext) {
	log := jctx.Logger

	rows, err := jctx.DBService.ListAttachmentsByTopicPrefix(
		ctx, attachments.EntityIncidents+"/",
		time.Now().Add(-attachmentOrphanGrace), attachmentSweepBatch,
	)
	if err != nil {
		log.WarnContext(ctx, "Failed to list incident attachments for GC", "error", err)

		return
	}

	swept := 0

	for _, row := range rows {
		if row.Topic == nil {
			continue
		}

		topic, parseErr := attachments.ParseTopic(*row.Topic)
		if parseErr != nil {
			// A malformed topic can only have come from an older or buggier
			// writer. Leave it alone rather than guess what it pointed at.
			continue
		}

		incident, incErr := jctx.DBService.GetIncidentAny(ctx, topic.EntityUID)
		if incErr == nil && incident != nil {
			continue
		}

		if !errors.Is(incErr, sql.ErrNoRows) {
			// A transient lookup failure must never be read as "the incident
			// is gone" — that would delete live evidence on a database blip.
			continue
		}

		if delErr := jctx.DBService.DeleteFile(ctx, row.OrganizationUID, row.UID); delErr != nil {
			log.WarnContext(ctx, "Failed to reap orphan attachment",
				"fileUid", row.UID, "error", delErr)

			continue
		}

		swept++
	}

	if swept > 0 {
		log.InfoContext(ctx, "Reaped orphan incident attachments", "count", swept)
	}
}
