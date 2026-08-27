package checkworker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/dbfault"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// errCheckJobsTableGone is the structural fault: the table the fetcher claims
// from is not there any more.
var errCheckJobsTableGone = errors.New("SQL logic error: no such table: check_jobs (1)")

// errClaimTimeout is an ordinary outage: the fetcher must keep retrying it.
var errClaimTimeout = errors.New("context deadline exceeded while claiming jobs")

// claimFailBackend is a WorkerBackend whose ClaimJobs always fails with a fixed
// error and counts the attempts. Everything else is an unused stub.
type claimFailBackend struct {
	err    error
	claims chan struct{}
	hints  chan string
}

func newClaimFailBackend(err error) *claimFailBackend {
	return &claimFailBackend{
		err:    err,
		claims: make(chan struct{}, 1000),
		hints:  make(chan string),
	}
}

func (b *claimFailBackend) ClaimJobs(
	context.Context, string, *string, int, int, time.Duration,
) ([]*models.CheckJob, time.Duration, error) {
	select {
	case b.claims <- struct{}{}:
	default:
	}

	return nil, 0, b.err
}

func (b *claimFailBackend) Register(_ context.Context, worker *models.Worker) (*models.Worker, error) {
	return worker, nil
}

func (b *claimFailBackend) Heartbeat(context.Context, string, []string, string) error {
	return nil
}

func (b *claimFailBackend) ClaimJobsForCheck(
	context.Context, string, *string, string,
) ([]*models.CheckJob, error) {
	return []*models.CheckJob{}, nil
}

func (b *claimFailBackend) SubmitResult(
	context.Context, *models.CheckJob, string, *backend.SubmitResultRequest,
) error {
	return nil
}

func (b *claimFailBackend) DeferRateLimited(context.Context, *models.CheckJob, string, time.Time) error {
	return nil
}

func (b *claimFailBackend) LastResults(
	context.Context, string, []string,
) (map[string]*models.Result, error) {
	return map[string]*models.Result{}, nil
}
func (b *claimFailBackend) Hints() <-chan string { return b.hints }

// newFetcherTestWorker builds a check worker whose fetcher is the only thing
// running, wired to a latch that captures the structural-fault log.
func newFetcherTestWorker(t *testing.T, claimErr error) (*CheckWorker, *claimFailBackend, *bytes.Buffer) {
	t.Helper()

	fake := newClaimFailBackend(claimErr)
	worker := newCheckWorker(&config.Config{}, fake)
	worker.setWorker(models.NewWorker("test-worker", "Test Worker"))
	worker.availableRunners.Store(1) // the fetcher only claims when a slot is free

	logs := &bytes.Buffer{}
	worker.SetFaultLatch(dbfault.NewLatch(slog.New(slog.NewTextHandler(logs, nil))))

	return worker, fake, logs
}

// TestFetcherStopsOnStructuralFault: the check worker shares the same
// classification as the job worker. A missing table is not something to keep
// fetching against.
func TestFetcherStopsOnStructuralFault(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	worker, _, logs := newFetcherTestWorker(t, errCheckJobsTableGone)

	fired := make(chan struct{})
	worker.faults.Arm(func() { close(fired) })

	// Never canceled: only the fault can end this loop.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})

	worker.wg.Add(1)

	go func() {
		defer close(done)
		worker.fetcherLoop(ctx)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		r.Fail("the fetcher must stop on a structural fault")
	}

	select {
	case <-fired:
	default:
		r.Fail("the terminal action must have been triggered")
	}

	fault := worker.faults.Fault()
	r.NotNil(fault)
	r.Equal("undefined_table", fault.Reason)
	r.Equal(1, strings.Count(logs.String(), dbfault.LogMessage))
	r.Contains(logs.String(), "component=check_worker")
}

// TestFetcherKeepsRetryingTransientErrors is the regression control: the
// fetcher's existing 5s retry must be untouched for errors that can recover.
func TestFetcherKeepsRetryingTransientErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	worker, fake, logs := newFetcherTestWorker(t, errClaimTimeout)

	var fired int
	worker.faults.Arm(func() { fired++ })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})

	worker.wg.Add(1)

	go func() {
		defer close(done)
		worker.fetcherLoop(ctx)
	}()

	// It keeps trying — and, crucially, it is still running.
	select {
	case <-fake.claims:
	case <-time.After(10 * time.Second):
		r.Fail("the fetcher must attempt a claim")
	}

	select {
	case <-done:
		r.Fail("a transient error must not stop the fetcher")
	case <-time.After(50 * time.Millisecond):
	}

	r.Zero(fired)
	r.Nil(worker.faults.Fault())
	r.Empty(logs.String())

	// Only its own context stops it.
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		r.Fail("the fetcher must stop when its context is canceled")
	}
}
