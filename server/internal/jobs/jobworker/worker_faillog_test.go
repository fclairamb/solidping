package jobworker

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/stats"
)

// TestHandleResultFailureLogLevel verifies that a failure about to be retried
// is logged at WARN, not ERROR: only a terminal unexpected failure is an ERROR.
func TestHandleResultFailureLogLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		jobErr     error
		retryCount int
		wantLine   string
	}{
		{
			name:     "retryable within budget",
			jobErr:   retryableErr(),
			wantLine: `level=WARN msg="Job failed, will retry"`,
		},
		{
			name:       "retryable but retries exhausted",
			jobErr:     retryableErr(),
			retryCount: jobsvc.MaxRetryCount,
			wantLine:   `level=ERROR msg="Job failed"`,
		},
		{
			name:     "permanent",
			jobErr:   jobdef.NewPermanentError(errTerminal),
			wantLine: `level=WARN msg="Job failed"`,
		},
		{
			name:     "unexpected terminal",
			jobErr:   errTerminal,
			wantLine: `level=ERROR msg="Job failed"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			logger := slog.New(slog.NewTextHandler(&buf, nil))
			w := &JobWorker{
				jobSvc: &fakeJobSvc{},
				logger: logger,
				stats:  stats.NewProcessingStats(time.Minute, time.Minute, logger),
			}

			job := models.NewJob(nil, "email")
			job.RetryCount = tt.retryCount

			_, err := w.handleResult(context.Background(), logger, job, tt.jobErr)
			require.NoError(t, err)
			require.Contains(t, buf.String(), tt.wantLine)

			if tt.wantLine[len("level="):len("level=")+4] == "WARN" {
				require.NotContains(t, buf.String(), "level=ERROR")
			}
		})
	}
}
