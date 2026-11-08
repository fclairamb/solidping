package jobdef

import "errors"

// RetryableError wraps an error to indicate it should be retried.
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string {
	return e.Err.Error()
}

func (e *RetryableError) Unwrap() error {
	return e.Err
}

// NewRetryableError creates a new retryable error.
func NewRetryableError(err error) *RetryableError {
	return &RetryableError{Err: err}
}

// IsRetryable checks if an error should trigger a retry.
func IsRetryable(err error) bool {
	var retryable *RetryableError
	return errors.As(err, &retryable)
}

// PermanentError wraps an error that retrying cannot fix (bad input). The
// worker records the job as failed and logs it at WARN, not ERROR.
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string {
	return e.Err.Error()
}

func (e *PermanentError) Unwrap() error {
	return e.Err
}

// NewPermanentError creates a new permanent error.
func NewPermanentError(err error) *PermanentError {
	return &PermanentError{Err: err}
}

// IsPermanent checks if an error is a permanent, non-retryable failure.
func IsPermanent(err error) bool {
	var permanent *PermanentError

	return errors.As(err, &permanent)
}
