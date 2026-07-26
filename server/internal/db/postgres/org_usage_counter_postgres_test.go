package postgres

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portOrgUsageCounter is distinct from every other _postgres_test.go embedded
// port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go; 15438-15448, 15451-15458 are claimed).
const portOrgUsageCounter = 15459

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
