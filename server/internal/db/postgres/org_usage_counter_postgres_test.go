package postgres

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// These are distinct from every other _postgres_test.go embedded port in the
// repo (see the port-numbering note in postgres_headroom_postgres_test.go;
// 15438-15448, 15451-15458 and 15461+ are claimed elsewhere).
const (
	portOrgUsageCounter          = 15459
	portOrgUsageCounterIncrement = 15460
)

// TestReserveMonthlyUsage_ConcurrentNeverOverruns_Postgres fires many
// concurrent reservations at a small monthly limit on a REAL Postgres backend
// and asserts the conditional upsert never grants more than the limit — the
// property SQLite's single-threaded tests can't prove. Self-skips under -short
// and on any embedded-startup error.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestReserveMonthlyUsage_ConcurrentNeverOverruns_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{Embedded: true, Port: portOrgUsageCounter, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("usage-cc-org", "Usage CC Org")
	r.NoError(s.CreateOrganization(ctx, org))

	const (
		limit      = 5
		goroutines = 50
		period     = "2026-07-01"
	)

	var granted atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, reserveErr := s.ReserveMonthlyUsage(ctx, org.UID, models.UsageCounterKindSMS, period, limit)
			if reserveErr == nil && ok {
				granted.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	r.Equal(int64(limit), granted.Load(),
		"concurrent reservations must never grant more than the monthly limit")

	count, err := s.GetMonthlyUsage(ctx, org.UID, models.UsageCounterKindSMS, period)
	r.NoError(err)
	r.Equal(limit, count, "the persisted counter must equal exactly the limit")
}

// TestIncrementUsageCounter_Concurrent_Postgres proves the unconditional
// upsert on a REAL Postgres backend: raw SQL (bun aliases the INSERT target,
// which breaks a real-name reference in the DO UPDATE) and no cap, so every
// caller must land. It is the counter behind the over-limit banner's
// `skippedToday` (spec 2026-08-26-03) — under-counting there means telling an
// org it lost fewer executions than it did.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestIncrementUsageCounter_Concurrent_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{Embedded: true, Port: portOrgUsageCounterIncrement, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("usage-inc-org", "Usage Inc Org")
	r.NoError(s.CreateOrganization(ctx, org))

	const (
		goroutines = 50
		period     = "2026-08-26"
	)

	var wg sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan error, goroutines)

	for range goroutines {
		wg.Add(1)

		go func() {
			defer wg.Done()
			<-start

			if incErr := s.IncrementUsageCounter(
				ctx, org.UID, models.UsageCounterKindCheckRateLimited, period,
			); incErr != nil {
				failures <- incErr
			}
		}()
	}

	close(start)
	wg.Wait()
	close(failures)

	for failure := range failures {
		r.NoError(failure, "an unconditional increment must never be refused")
	}

	count, err := s.GetMonthlyUsage(ctx, org.UID, models.UsageCounterKindCheckRateLimited, period)
	r.NoError(err)
	r.Equal(goroutines, count, "every recorded skip must be counted exactly once")

	// A different day is a different bucket — the banner clears at UTC midnight.
	other, err := s.GetMonthlyUsage(ctx, org.UID, models.UsageCounterKindCheckRateLimited, "2026-08-25")
	r.NoError(err)
	r.Zero(other)
}
