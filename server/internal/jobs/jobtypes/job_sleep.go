package jobtypes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// SleepJobConfig is the configuration for a sleep job.
type SleepJobConfig struct {
	Seconds int `json:"seconds"`
}

// SleepJobDefinition is the factory for sleep jobs.
type SleepJobDefinition struct{}

// Type returns the job type for sleep jobs.
func (d *SleepJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeSleep
}

// CreateJobRun creates a new sleep job run from the given configuration.
func (d *SleepJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg SleepJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, err
	}

	// Default to 5 seconds if not specified
	if cfg.Seconds == 0 {
		cfg.Seconds = 5
	}

	return &SleepJobRun{config: cfg}, nil
}

// SleepJobRun is an executable sleep job instance.
type SleepJobRun struct {
	config SleepJobConfig
}

// Run executes the sleep job.
func (r *SleepJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	log.InfoContext(ctx, "Starting sleep job", "duration_seconds", r.config.Seconds)

	select {
	case <-ctx.Done():
		log.WarnContext(ctx, "Sleep job interrupted by context cancellation")
		return ctx.Err()
	case <-time.After(time.Duration(r.config.Seconds) * time.Second):
		log.InfoContext(ctx, "Sleep completed successfully")
		return nil
	}
}
