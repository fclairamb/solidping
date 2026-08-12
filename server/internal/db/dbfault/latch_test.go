package dbfault_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/dbfault"
)

// Static errors keep the table cases err113-clean.
var (
	errDialRefused    = errors.New("dial tcp: connection refused")
	errPlainTransient = errors.New("transient")
)

func newTestLatch(t *testing.T) (*dbfault.Latch, *bytes.Buffer) {
	t.Helper()

	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	return dbfault.NewLatch(logger), buf
}

func structuralErr() error {
	return &pq.Error{Code: "42P01", Message: `relation "jobs" does not exist`}
}

func TestLatchTransientErrorsChangeNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	latch, buf := newTestLatch(t)

	var fired int
	latch.Arm(func() { fired++ })

	// The regression guard: a dropped connection must not latch, must not log,
	// and must not trigger the terminal action — the caller retries as before.
	for _, err := range []error{
		nil,
		context.Canceled,
		errDialRefused,
		&pq.Error{Code: "08006", Message: "connection failure"},
		&pq.Error{Code: "40001", Message: "serialization failure"},
	} {
		r.False(latch.Report(t.Context(), err))
	}

	r.Zero(fired)
	r.Nil(latch.Fault())
	r.NoError(latch.Err())
	r.Empty(buf.String())
}

func TestLatchStructuralErrorFiresOnceAndLogsOnce(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	latch, buf := newTestLatch(t)

	var fired int
	latch.Arm(func() { fired++ })

	r.True(latch.Report(t.Context(), structuralErr(), "component", "job_worker"))

	// Same fault reported another 50 times: still one action, still one line.
	for range 50 {
		r.True(latch.Report(t.Context(), structuralErr(), "component", "job_worker"))
	}

	r.Equal(1, fired)
	r.Equal(1, strings.Count(buf.String(), dbfault.LogMessage),
		"exactly one clear line naming the fault, not a stream of query errors")
	r.Contains(buf.String(), "reason=undefined_table")
	r.Contains(buf.String(), "code=42P01")
	r.Contains(buf.String(), "component=job_worker")

	fault := latch.Fault()
	r.NotNil(fault)
	r.Equal("undefined_table", fault.Reason)
	r.ErrorContains(latch.Err(), "undefined_table")
}

func TestLatchArmAfterFaultFiresImmediately(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	latch, _ := newTestLatch(t)

	// A runner can fail before the server has finished wiring its shutdown
	// hook; the fault must not be swallowed by that race.
	r.True(latch.Report(t.Context(), structuralErr()))

	fired := make(chan struct{}, 1)
	latch.Arm(func() { fired <- struct{}{} })

	select {
	case <-fired:
	default:
		r.Fail("Arm must run the action immediately when a fault is already latched")
	}
}

func TestLatchNilReceiverIsSafe(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var latch *dbfault.Latch

	r.NotPanics(func() {
		latch.Arm(func() {})
		r.True(latch.Report(t.Context(), structuralErr()))
		r.False(latch.Report(t.Context(), errPlainTransient))
		r.Nil(latch.Fault())
		r.NoError(latch.Err())
	})
}

func TestLatchIsConcurrencySafe(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	latch, buf := newTestLatch(t)

	var (
		mu    sync.Mutex
		fired int
	)

	latch.Arm(func() {
		mu.Lock()
		defer mu.Unlock()
		fired++
	})

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 20 {
				latch.Report(context.Background(), structuralErr())
			}
		}()
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	r.Equal(1, fired)
	r.Equal(1, strings.Count(buf.String(), dbfault.LogMessage))
}
