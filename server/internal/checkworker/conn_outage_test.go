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
