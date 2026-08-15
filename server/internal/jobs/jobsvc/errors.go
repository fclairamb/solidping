// Package jobsvc provides job queue operations for the job system.
package jobsvc

import "errors"

var (
	// ErrJobNotFound is returned when a job cannot be found.
	ErrJobNotFound = errors.New("job not found")

	// ErrInvalidJobConfig is returned when a job's config payload is not a
	// JSON object. It is a client error, not an infrastructure failure — HTTP
	// callers must map it to 400, which is why it is a sentinel rather than an
	// anonymous fmt.Errorf that could only be matched on its message.
	ErrInvalidJobConfig = errors.New("invalid job config")

	// ErrJobAlreadyCompleted is returned when trying to cancel a completed job.
	ErrJobAlreadyCompleted = errors.New("job not found or already completed")

	// ErrExecutionNotFound is returned when an execution cannot be found.
	ErrExecutionNotFound = errors.New("execution not found")

	// ErrJobNotPending is returned when trying to cancel a non-pending job.
	ErrJobNotPending = errors.New("job not found or not pending")

	// ErrMaxRetriesReached is returned when a job has reached its retry limit.
	ErrMaxRetriesReached = errors.New("max retries reached (3 attempts total)")

	// ErrJobLeaseLost is returned by CompleteRunningJob when the job is no
	// longer in 'running' — the stuck-job reaper (or another actor) already
	// transitioned it, so the worker's terminal write must be discarded rather
	// than clobbering the reaper's decision.
	ErrJobLeaseLost = errors.New("job no longer running, terminal write discarded")
)
