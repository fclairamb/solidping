package jobworker

import (
	"context"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// defaultQueueDepthInterval is how often the queue-depth sampler runs. A
// constant for v1 — no config key needed (per spec).
const defaultQueueDepthInterval = 30 * time.Second

// QueueDepthSampler periodically samples the background-job queue depth and
// publishes it to the solidping_jobs_queue_depth gauge. It runs one cheap
// COUNT(*) ... GROUP BY status query per tick and zero-fills absent statuses so
// a drained status drops to 0 rather than going stale.
type QueueDepthSampler struct {
	jobSvc   jobsvc.Service
	interval time.Duration
	logger   *slog.Logger
}

// NewQueueDepthSampler creates a queue-depth sampler with the default interval.
func NewQueueDepthSampler(jobSvc jobsvc.Service) *QueueDepthSampler {
	return &QueueDepthSampler{
		jobSvc:   jobSvc,
		interval: defaultQueueDepthInterval,
		logger:   slog.Default().With("component", "job_queue_depth_sampler"),
	}
}

// Run samples once immediately, then on every tick until ctx is cancelled.
// It always returns ctx.Err() so callers can ignore context.Canceled.
func (s *QueueDepthSampler) Run(ctx context.Context) error {
	s.logger.InfoContext(ctx, "Starting job queue-depth sampler", "interval", s.interval)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Prime the gauge immediately so it isn't absent for the first interval.
	s.sample(ctx)

	for {
		select {
		case <-ctx.Done():
			s.logger.InfoContext(ctx, "Job queue-depth sampler stopped")

			return ctx.Err()
		case <-ticker.C:
			s.sample(ctx)
		}
	}
}

// sample runs one query and updates the gauge. Errors are logged, not fatal —
// a transient DB hiccup should not kill the sampler.
func (s *QueueDepthSampler) sample(ctx context.Context) {
	depth, err := s.jobSvc.CountQueueDepth(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "Failed to sample job queue depth", "error", err)

		return
	}

	for status, count := range depth {
		prommetrics.SetJobsQueueDepth(string(status), float64(count))
	}

	s.logger.DebugContext(ctx, "Sampled job queue depth",
		"pending", depth[models.JobStatusPending],
		"running", depth[models.JobStatusRunning])
}
