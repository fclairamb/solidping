package jobdef

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryableError wraps an error to indicate it should be retried.
type RetryableError struct {
	Err error
	// After is an optional hint (e.g. a Retry-After header) for the minimum
	// delay before the retry. Zero means "use the default backoff".
	After time.Duration
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

// RetryAfterHint returns the retry delay hint carried by a retryable error, or
// zero when there is none.
func RetryAfterHint(err error) time.Duration {
	var retryable *RetryableError
	if errors.As(err, &retryable) {
		return retryable.After
	}

	return 0
}

// MaxRetryAfter caps a Retry-After hint: it is the last backoff step.
const MaxRetryAfter = 15 * time.Minute

// HTTPStatusError reports a non-success HTTP response of a delivery.
type HTTPStatusError struct {
	StatusCode int
	RetryAfter time.Duration
	Err        error
}

func (e *HTTPStatusError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("HTTP status %d", e.StatusCode)
	}

	return e.Err.Error()
}

func (e *HTTPStatusError) Unwrap() error {
	return e.Err
}

// HTTPStatusCode returns the HTTP status of the failed response.
func (e *HTTPStatusError) HTTPStatusCode() int {
	return e.StatusCode
}

// HTTPStatusCodeOf returns the HTTP status carried by err, or 0 when err does
// not come from an HTTP response.
func HTTPStatusCodeOf(err error) int {
	var coded interface{ HTTPStatusCode() int }
	if errors.As(err, &coded) {
		return coded.HTTPStatusCode()
	}

	return 0
}

// ClassifyHTTPStatus wraps err (the description of a non-2xx response) into a
// typed HTTPStatusError, marked retryable for 408, 429 and 5xx (the receiver
// may recover) and permanent for any other status (the request itself is wrong).
func ClassifyHTTPStatus(code int, retryAfter time.Duration, err error) error {
	statusErr := &HTTPStatusError{StatusCode: code, RetryAfter: retryAfter, Err: err}

	if code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= http.StatusInternalServerError {
		retryable := NewRetryableError(statusErr)
		retryable.After = retryAfter

		return retryable
	}

	return NewPermanentError(statusErr)
}

// ParseRetryAfter reads a Retry-After header value (delay in seconds or HTTP
// date), clamped to [0, MaxRetryAfter]. Invalid or empty values give zero.
func ParseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}

	var wait time.Duration

	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		wait = time.Duration(seconds * float64(time.Second))
	} else if when, dateErr := http.ParseTime(value); dateErr == nil {
		wait = when.Sub(now)
	}

	if wait < 0 {
		return 0
	}

	return min(wait, MaxRetryAfter)
}
