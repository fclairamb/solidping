// Package jobsvc provides job queue operations for the job system.
package jobsvc

import "errors"

var (
	// ErrJobNotFound is returned when a job cannot be found.
	ErrJobNotFound = errors.New("job not found")

	// ErrJobAlreadyCompleted is returned when trying to cancel a completed job.
	ErrJobAlreadyCompleted = errors.New("job not found or already completed")

	// ErrExecutionNotFound is returned when an execution cannot be found.
	ErrExecutionNotFound = errors.New("execution not found")

	// ErrJobNotPending is returned when trying to cancel a non-pending job.
	ErrJobNotPending = errors.New("job not found or not pending")

	// ErrMaxRetriesReached is returned when a job has reached its retry limit.
	ErrMaxRetriesReached = errors.New("max retries reached (3 attempts total)")
)
