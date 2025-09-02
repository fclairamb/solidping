package jobworker

import "errors"

var (
	// ErrJobPanic is returned when a job panics during execution.
	ErrJobPanic = errors.New("job panicked during execution")

	// ErrUnknownJobType is returned when a job type is not registered in the registry.
	ErrUnknownJobType = errors.New("unknown job type")
)
