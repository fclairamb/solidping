package jobsvc_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

func TestRetryJob_AfterHint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		retryCount int
		after      time.Duration
		wantMin    time.Duration
		wantMax    time.Duration
	}{
		{"no hint keeps the backoff", 0, 0, 1 * time.Minute, 1*time.Minute + 30*time.Second},
		{"hint longer than backoff wins", 0, 10 * time.Minute, 10 * time.Minute, 10*time.Minute + 30*time.Second},
		{"hint is capped at 15 minutes", 0, 2 * time.Hour, 15 * time.Minute, 15*time.Minute + 30*time.Second},
		{"hint shorter than backoff keeps the backoff", 1, 30 * time.Second, 5 * time.Minute, 5*time.Minute + 30*time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			require.NoError(t, err)
			require.NoError(t, dbSvc.Initialize(ctx))
			t.Cleanup(func() { _ = dbSvc.Close() })

			svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

			job := models.NewJob(nil, "email")
			job.Status = models.JobStatusRunning
			job.RetryCount = tt.retryCount
			_, err = dbSvc.DB().NewInsert().Model(job).Exec(ctx)
			require.NoError(t, err)

			start := time.Now()
			clone, err := svc.RetryJob(ctx, job, tt.after)
			require.NoError(t, err)

			delay := clone.ScheduledAt.Sub(start)
			require.GreaterOrEqual(t, delay, tt.wantMin-time.Second)
			require.Less(t, delay, tt.wantMax)
			require.Equal(t, tt.retryCount+1, clone.RetryCount)
		})
	}
}
