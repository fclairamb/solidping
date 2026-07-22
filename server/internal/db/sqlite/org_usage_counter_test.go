package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func setupCounterDB(t *testing.T) *Service {
	t.Helper()

	ctx := context.Background()
	dbSvc, err := New(ctx, Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return dbSvc
}

func TestReserveMonthlyUsage_IncrementAndBoundary(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	db := setupCounterDB(t)
	const org, kind, period, limit = "org-1", "sms", "2026-07-01", 3

	for i := 1; i <= limit; i++ {
		ok, err := db.ReserveMonthlyUsage(ctx, org, kind, period, limit)
		r.NoError(err)
		r.True(ok, "reservation %d within limit must succeed", i)

		count, gErr := db.GetMonthlyUsage(ctx, org, kind, period)
		r.NoError(gErr)
		r.Equal(i, count)
	}

	// One past the limit is denied and does not increment.
	ok, err := db.ReserveMonthlyUsage(ctx, org, kind, period, limit)
	r.NoError(err)
	r.False(ok, "reservation over the limit must be denied")

	count, err := db.GetMonthlyUsage(ctx, org, kind, period)
	r.NoError(err)
	r.Equal(limit, count, "denied reservation must not increment the counter")
}

func TestReserveMonthlyUsage_MonthRollover(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	db := setupCounterDB(t)
	const org, kind, limit = "org-1", "sms", 1

	ok, err := db.ReserveMonthlyUsage(ctx, org, kind, "2026-07-01", limit)
	r.NoError(err)
	r.True(ok)

	// July is now exhausted...
	ok, err = db.ReserveMonthlyUsage(ctx, org, kind, "2026-07-01", limit)
	r.NoError(err)
	r.False(ok)

	// ...but August is a fresh period.
	ok, err = db.ReserveMonthlyUsage(ctx, org, kind, "2026-08-01", limit)
	r.NoError(err)
	r.True(ok, "a new month starts a fresh counter")
}

func TestReserveMonthlyUsage_KindsAreIndependent(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	db := setupCounterDB(t)
	const org, period, limit = "org-1", "2026-07-01", 1

	ok, err := db.ReserveMonthlyUsage(ctx, org, "sms", period, limit)
	r.NoError(err)
	r.True(ok)

	// Exhausting sms must not affect voice.
	ok, err = db.ReserveMonthlyUsage(ctx, org, "voice", period, limit)
	r.NoError(err)
	r.True(ok)
}

func TestGetMonthlyUsage_NoRowIsZero(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	db := setupCounterDB(t)
	count, err := db.GetMonthlyUsage(ctx, "org-x", "sms", "2026-07-01")
	r.NoError(err)
	r.Equal(0, count)
}
