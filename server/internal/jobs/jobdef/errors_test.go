package jobdef

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

var errBadInput = errors.New("bad input")

func TestPermanentError(t *testing.T) {
	t.Parallel()

	base := errBadInput
	err := fmt.Errorf("wrapped: %w", NewPermanentError(base))

	require.True(t, IsPermanent(err))
	require.False(t, IsRetryable(err))
	require.ErrorIs(t, err, base)
	require.False(t, IsPermanent(base))
}

func TestClassifyHTTPStatus(t *testing.T) {
	t.Parallel()

	base := errBoom

	for _, code := range []int{408, 429, 500, 502, 503} {
		err := ClassifyHTTPStatus(code, 30*time.Second, base)
		require.True(t, IsRetryable(err), code)
		require.False(t, IsPermanent(err), code)
		require.ErrorIs(t, err, base)
		require.Equal(t, code, HTTPStatusCodeOf(err))
		require.Equal(t, 30*time.Second, RetryAfterHint(err))
	}

	for _, code := range []int{301, 400, 404, 405, 410} {
		err := ClassifyHTTPStatus(code, 0, base)
		require.True(t, IsPermanent(err), code)
		require.False(t, IsRetryable(err), code)
		require.ErrorIs(t, err, base)
		require.Equal(t, code, HTTPStatusCodeOf(err))
	}

	require.Equal(t, 0, HTTPStatusCodeOf(base))
	require.Equal(t, time.Duration(0), RetryAfterHint(base))
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	require.Equal(t, time.Duration(0), ParseRetryAfter("", now))
	require.Equal(t, time.Duration(0), ParseRetryAfter("soon", now))
	require.Equal(t, time.Duration(0), ParseRetryAfter("-5", now))
	require.Equal(t, 30*time.Second, ParseRetryAfter("30", now))
	require.Equal(t, MaxRetryAfter, ParseRetryAfter("86400", now))
	require.Equal(t, time.Minute, ParseRetryAfter(now.Add(time.Minute).Format(http.TimeFormat), now))
	require.Equal(t, time.Duration(0), ParseRetryAfter(now.Add(-time.Minute).Format(http.TimeFormat), now))
}
