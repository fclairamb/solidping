package checkworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

var errClosedConn = errors.New("write tcp: use of closed network connection")

func abandonedCount(checkType, reason string) float64 {
	return testutil.ToFloat64(prommetrics.CheckRunnerAbandoned.WithLabelValues(checkType, reason))
}

func countLevel(logs, level string) int {
	return strings.Count(logs, "level="+level)
}

// TestConnLossDuringClaimLogsOneWarn: repeated claim failures carrying the
// agent connection-lost sentinel yield no ERROR and exactly one WARN; the
// recovery is summarized once the connection is back.
func TestConnLossDuringClaimLogsOneWarn(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := newClaimFailBackend(fmt.Errorf("claim: %w", backend.ErrConnLost))
	worker := newCheckWorker(&config.Config{}, fake)
	worker.setWorker(models.NewWorker("w", "W"))
	worker.availableRunners.Store(1)

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))

	for range 5 {
		_, err := worker.fetchAndDistributeJobs(context.Background(), logger)
		r.ErrorIs(err, backend.ErrConnLost)
	}

	r.Zero(countLevel(logs.String(), "ERROR"))
	r.Equal(1, countLevel(logs.String(), "WARN"))
	r.True(worker.outage.active())

	worker.outage.noteRecovered(context.Background(), logger)
	r.False(worker.outage.active())
	r.Contains(logs.String(), "failures_during_outage=5")
	r.Zero(countLevel(logs.String(), "ERROR"))
}

// TestPlainClaimFailureStaysError is the control: other failures keep ERROR.
func TestPlainClaimFailureStaysError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	worker := newCheckWorker(&config.Config{}, newClaimFailBackend(errClaimTimeout))
	worker.setWorker(models.NewWorker("w", "W"))
	worker.availableRunners.Store(1)

	logs := &bytes.Buffer{}
	_, err := worker.fetchAndDistributeJobs(context.Background(), slog.New(slog.NewTextHandler(logs, nil)))
	r.Error(err)
	r.Equal(1, countLevel(logs.String(), "ERROR"))
}

func TestIsConnLoss(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(isConnLoss(fmt.Errorf("failed to submit result: %w", backend.ErrConnLost)))
	r.True(isConnLoss(errClosedConn))
	r.False(isConnLoss(errClaimTimeout))
	r.False(isConnLoss(nil))
}

// TestAbandonDuringOutageIsWarnWithDistinctReason: a checker cut off during a
// connection outage is WARN + reason=connection_lost; outside an outage it
// stays ERROR + reason=ignored_context.
func TestAbandonDuringOutageIsWarnWithDistinctReason(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	worker := newCheckWorker(&config.Config{}, newClaimFailBackend(nil))
	job := testCheckJob("test-conn-lost-abandon")

	lostBefore := abandonedCount(job.Type, prommetrics.AbandonReasonConnectionLost)
	ignoredBefore := abandonedCount(job.Type, prommetrics.AbandonReasonIgnoredContext)

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))

	worker.outage.noteLoss(context.Background(), logger, "claim", backend.ErrConnLost)
	logs.Reset()

	_, err := worker.abandonCheckerExecution(context.Background(), logger, job, time.Second)
	r.NoError(err)
	r.Zero(countLevel(logs.String(), "ERROR"))
	r.Equal(1, countLevel(logs.String(), "WARN"))
	r.Contains(logs.String(), "reason=connection_lost")

	worker.outage.noteRecovered(context.Background(), logger)
	logs.Reset()

	_, err = worker.abandonCheckerExecution(context.Background(), logger, job, time.Second)
	r.NoError(err)
	r.Equal(1, countLevel(logs.String(), "ERROR"))
	r.Contains(logs.String(), "reason=ignored_context")

	r.InDelta(lostBefore+1, abandonedCount(job.Type, prommetrics.AbandonReasonConnectionLost), 0.01)
	r.InDelta(ignoredBefore+1, abandonedCount(job.Type, prommetrics.AbandonReasonIgnoredContext), 0.01)
}

// timeoutNetError is a driver read deadline, as pgdriver reports it on a slow
// database link (`read tcp …: i/o timeout`).
type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "read tcp 10.0.0.1:5432: i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

// TestClaimTimeoutIsWarnAndCounted: a claim that times out on a slow database
// link (spec 2026-11-11-06) is a WARN with the elapsed time, counted under
// outcome=timeout, not an ERROR — and the fetcher still retries it
// (TestFetcherKeepsRetryingTransientErrors covers the retry loop).
func TestClaimTimeoutIsWarnAndCounted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	claimErr := fmt.Errorf("failed to claim jobs: %w", timeoutNetError{})
	worker := newCheckWorker(&config.Config{}, newClaimFailBackend(claimErr))
	worker.setWorker(models.NewWorker("w", "W"))
	worker.availableRunners.Store(1)

	before := testutil.ToFloat64(prommetrics.ClaimJobsResult.WithLabelValues("timeout"))

	logs := &bytes.Buffer{}
	_, err := worker.fetchAndDistributeJobs(context.Background(), slog.New(slog.NewTextHandler(logs, nil)))
	r.Error(err)
	r.Zero(countLevel(logs.String(), "ERROR"))
	r.Equal(1, countLevel(logs.String(), "WARN"))
	r.Contains(logs.String(), "Claim timed out")
	r.Contains(logs.String(), "elapsed=")
	r.GreaterOrEqual(testutil.ToFloat64(prommetrics.ClaimJobsResult.WithLabelValues("timeout"))-before, 1.0)
}

func TestIsClaimTimeout(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(isClaimTimeout(fmt.Errorf("claim: %w", timeoutNetError{})))
	r.True(isClaimTimeout(fmt.Errorf("claim: %w", context.DeadlineExceeded)))
	r.True(isClaimTimeout(errors.New("write tcp 10.0.0.1:5432: i/o timeout")))
	r.False(isClaimTimeout(errClaimTimeout))
	r.False(isClaimTimeout(errClosedConn))
}
