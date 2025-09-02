package jobtypes

import (
	"context"
	"encoding/json"
	"time"

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
