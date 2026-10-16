package db_test

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portGlobalCounterPG is distinct from every other embedded-Postgres port
// claimed in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portGlobalCounterPG = 15760

// globalCounterBurst is how many goroutines race one counter: well above the
// limit, so a lost update would admit more than `limit` of them.
const globalCounterBurst = 40

// TestBumpGlobalCounter_SQLite proves the atomic global counter on SQLite.
func TestBumpGlobalCounter_SQLite(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "sqlite-global-counter-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	svc, err := sqlite.New(t.Context(), sqlite.Config{DataDir: tempDir})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(t.Context()))

	testBumpGlobalCounter(t, svc)
}

// TestBumpGlobalCounter_Postgres is the real-engine twin: Postgres has a
// connection pool with real concurrency, so serialization there rests on the
// per-key advisory lock rather than on a single connection.
func TestBumpGlobalCounter_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	svc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portGlobalCounterPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	testBumpGlobalCounter(t, svc)
}

// burstGlobalCounter fires n concurrent bumps and returns how many were
// admitted and how many refused.
func burstGlobalCounter(
	ctx context.Context, t *testing.T, svc db.Service, key string, limit, n int,
) (int, int) {
	t.Helper()

	var (
		wg       sync.WaitGroup
		admitted atomic.Int32
		refused  atomic.Int32
		errCount atomic.Int32
	)

	start := make(chan struct{})

	for range n {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			limited, err := svc.BumpGlobalCounter(ctx, key, limit, time.Minute)

			switch {
			case err != nil:
				errCount.Add(1)
			case limited:
				refused.Add(1)
			default:
				admitted.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	require.Zero(t, errCount.Load(), "no concurrent bump may fail")

	return int(admitted.Load()), int(refused.Load())
}

// liveCounterRows lists the live global rows of key.
func liveCounterRows(ctx context.Context, t *testing.T, svc db.Service, key string) []*models.StateEntry {
	t.Helper()

	entries, err := svc.ListStateEntries(ctx, nil, key)
	require.NoError(t, err)

	live := make([]*models.StateEntry, 0, len(entries))

	for _, entry := range entries {
		if entry.Key == key && entry.DeletedAt == nil &&
			(entry.ExpiresAt == nil || entry.ExpiresAt.After(time.Now())) {
			live = append(live, entry)
		}
	}

	return live
}

// testBumpGlobalCounter is the engine-agnostic contract.
//
//nolint:funlen // one table of cases sharing the same helpers
func testBumpGlobalCounter(t *testing.T, svc db.Service) {
	t.Helper()

	ctx := t.Context()
	expired := -time.Minute

	tests := []struct {
		name string
		run  func(t *testing.T, key string)
	}{
		{
			name: "burst on a fresh key admits exactly limit",
			run: func(t *testing.T, key string) {
				admitted, refused := burstGlobalCounter(ctx, t, svc, key, 5, globalCounterBurst)
				require.Equal(t, 5, admitted)
				require.Equal(t, globalCounterBurst-5, refused)

				live := liveCounterRows(ctx, t, svc, key)
				require.Len(t, live, 1, "a burst on a fresh key must create exactly one row")
				require.Equal(t, 5, models.CounterValue(live[0]))
			},
		},
		{
			name: "burst on an existing row admits only the remaining budget",
			run: func(t *testing.T, key string) {
				ttl := time.Minute
				require.NoError(t, svc.SetStateEntry(ctx, nil, key, models.NewCounterValue(2), &ttl))

				admitted, refused := burstGlobalCounter(ctx, t, svc, key, 5, globalCounterBurst)
				require.Equal(t, 3, admitted)
				require.Equal(t, globalCounterBurst-3, refused)

				live := liveCounterRows(ctx, t, svc, key)
				require.Len(t, live, 1)
				require.Equal(t, 5, models.CounterValue(live[0]))
			},
		},
		{
			name: "expired row counts as zero",
			run: func(t *testing.T, key string) {
				require.NoError(t, svc.SetStateEntry(ctx, nil, key, models.NewCounterValue(5), &expired))

				limited, err := svc.BumpGlobalCounter(ctx, key, 5, time.Minute)
				require.NoError(t, err)
				require.False(t, limited)

				live := liveCounterRows(ctx, t, svc, key)
				require.Len(t, live, 1)
				require.Equal(t, 1, models.CounterValue(live[0]))
			},
		},
		{
			name: "soft-deleted row counts as zero",
			run: func(t *testing.T, key string) {
				ttl := time.Minute
				require.NoError(t, svc.SetStateEntry(ctx, nil, key, models.NewCounterValue(5), &ttl))

				deleted, err := svc.DeleteStateEntry(ctx, nil, key)
				require.NoError(t, err)
				require.True(t, deleted)

				limited, err := svc.BumpGlobalCounter(ctx, key, 5, time.Minute)
				require.NoError(t, err)
				require.False(t, limited)

				live := liveCounterRows(ctx, t, svc, key)
				require.Len(t, live, 1)
				require.Equal(t, 1, models.CounterValue(live[0]))
			},
		},
		{
			name: "legacy duplicate rows: the largest count wins and all are rewritten",
			run: func(t *testing.T, key string) {
				// SetStateEntry upserts, so duplicates are planted the way the
				// old read-modify-write left them: two plain inserts.
				for _, n := range []int{3, 4} {
					ttl := time.Now().Add(time.Minute)
					entry := models.NewStateEntry(nil, key)
					entry.Value = models.NewCounterValue(n)
					entry.ExpiresAt = &ttl
					require.NoError(t, insertRawStateEntry(ctx, svc, entry))
				}

				limited, err := svc.BumpGlobalCounter(ctx, key, 5, time.Minute)
				require.NoError(t, err)
				require.False(t, limited)

				live := liveCounterRows(ctx, t, svc, key)
				require.Len(t, live, 2)

				for _, entry := range live {
					require.Equal(t, 5, models.CounterValue(entry))
				}

				limited, err = svc.BumpGlobalCounter(ctx, key, 5, time.Minute)
				require.NoError(t, err)
				require.True(t, limited)
			},
		},
		{
			name: "sequential bumps admit exactly limit then refuse",
			run: func(t *testing.T, key string) {
				for i := range 3 {
					limited, err := svc.BumpGlobalCounter(ctx, key, 3, time.Minute)
					require.NoError(t, err)
					require.False(t, limited, "bump %d must be admitted", i+1)
				}

				limited, err := svc.BumpGlobalCounter(ctx, key, 3, time.Minute)
				require.NoError(t, err)
				require.True(t, limited)

				live := liveCounterRows(ctx, t, svc, key)
				require.Len(t, live, 1)
				require.Equal(t, 3, models.CounterValue(live[0]), "a refused bump writes nothing")
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t, "global-counter-test:"+string(rune('a'+i)))
		})
	}
}

// insertRawStateEntry inserts a state entry as-is, without SetStateEntry's
// update-first upsert, to plant the duplicates older code could leave behind.
func insertRawStateEntry(ctx context.Context, svc db.Service, entry *models.StateEntry) error {
	_, err := svc.DB().NewInsert().Model(entry).Exec(ctx)

	return err
}
