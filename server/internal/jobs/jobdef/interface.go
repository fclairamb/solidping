package jobdef

import (
	"context"
	"encoding/json"
)

// JobDefinition is a factory for creating job instances.
// Each job type must implement this interface and register itself.
type JobDefinition interface {
	// Type returns the unique identifier for this job type
	Type() JobType

	// CreateJobRun creates a new JobRun instance from the job config
	CreateJobRun(config json.RawMessage) (JobRunner, error)
}

// JobRunner represents an executable job instance.
type JobRunner interface {
	// Run executes the job with the given context.
	// Return nil for success, error for failure.
	// Return RetryableError to trigger retry.
	Run(ctx context.Context, jctx *JobContext) error
}
