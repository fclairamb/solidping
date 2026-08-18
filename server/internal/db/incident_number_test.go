package db_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// portIncidentNumberPG is distinct from every other embedded-Postgres port
// claimed in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portIncidentNumberPG = 15470

// concurrentIncidentCreations is how many incidents the race test opens at
// once — enough that MAX(number)+1 reads overlap. The Postgres connection pool
// is bounded below the embedded server's max_connections (see below), so extra
// goroutines queue on the pool instead of being refused by the server.
//
// It is deliberately set WELL ABOVE db.IncidentNumberAttempts. At 12 this test
// was green on an idle machine and failed on a loaded CI runner, because the
// unlucky goroutine loses one race per competing commit and the retry budget
// used to be spent by those legitimate losses: N racers need up to N-1 retries.
// Anything at or below the budget could never catch that, so the count is what
// makes the stall-versus-progress distinction load-bearing here.
//
// Whether the unique-violation RETRY fires on a given run is up to the
// scheduler, so it is pinned deterministically by
// TestCreateIncidentWithNumber_RetriesOnUniqueViolation rather than left to
// this test's timing.
const concurrentIncidentCreations = 32

// TestIncidentNumbers_SQLite proves the per-org numbering contract on SQLite.
func TestIncidentNumbers_SQLite(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "sqlite-incident-number-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	svc, err := sqlite.New(t.Context(), sqlite.Config{DataDir: tempDir})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(t.Context()))

	testIncidentNumbers(t, svc)
}

// TestIncidentNumbers_Postgres is the real-engine twin. The MAX+1 read races
// differently there (a connection pool with real concurrency, versus SQLite's
// single writer), so both engines need their own proof — a scheme that only
// held because SQLite serializes writes would be a production bug.
func TestIncidentNumbers_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	svc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portIncidentNumberPG,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	// The embedded server runs with max_connections=10 and the embedded
	// constructor leaves the pool unbounded, so the racing goroutines would each
	// open their own connection and be refused with "sorry, too many clients
	// already" long before the server could answer a duplicate key — masking the
	// very contention this test exists to create. Bounded below 10, the extra
	// goroutines queue on the pool instead.
	svc.DB().SetMaxOpenConns(6)

	testIncidentNumbers(t, svc)
}

// testIncidentNumbers is the engine-agnostic contract, run against both
// backends.
func testIncidentNumbers(t *testing.T, svc db.Service) {
	t.Helper()

	ctx := t.Context()

	t.Run("ParallelCreationsGetDistinctSequentialNumbers", func(t *testing.T) {
		t.Parallel()
		testParallelIncidentNumbers(ctx, t, svc)
	})

	t.Run("NumbersArePerOrganization", func(t *testing.T) {
		t.Parallel()
		testIncidentNumbersArePerOrg(ctx, t, svc)
	})

	t.Run("LookupByNumber", func(t *testing.T) {
		t.Parallel()
		testGetIncidentByNumber(ctx, t, svc)
	})

	t.Run("RealDriverCollisionIsClassifiedAsRetryable", func(t *testing.T) {
		t.Parallel()
		testRealNumberCollisionIsClassified(ctx, t, svc)
	})
}

// seedIncidentOrg creates an org plus one check to hang incidents off.
func seedIncidentOrg(ctx context.Context, t *testing.T, svc db.Service, slug string) (string, string) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization(slug, slug)
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, slug+"-check", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	return org.UID, check.UID
}

// testParallelIncidentNumbers is the race proof: N goroutines opening incidents
// in the SAME organization at the same time must produce exactly the numbers
// 1..N, each used once. A lost update would show up as a duplicate; a swallowed
// unique violation as a gap or an error.
func testParallelIncidentNumbers(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)
	orgUID, checkUID := seedIncidentOrg(ctx, t, svc, "inc-num-race")

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		numbers []int64
		errs    []error
	)

	start := make(chan struct{})

	for i := 0; i < concurrentIncidentCreations; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			incident := models.NewIncident(orgUID, checkUID, time.Now(), "race")

			err := svc.CreateIncident(ctx, incident)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errs = append(errs, err)

				return
			}

			numbers = append(numbers, incident.Number)
		}()
	}

	close(start)
	wg.Wait()

	r.Empty(errs, "no concurrent creation may fail")
	r.Len(numbers, concurrentIncidentCreations)

	seen := make(map[int64]bool, len(numbers))
	for _, number := range numbers {
		r.Positive(number, "every incident must get a number")
		r.False(seen[number], "number %d was handed out twice", number)
		seen[number] = true
	}

	for i := int64(1); i <= concurrentIncidentCreations; i++ {
		r.True(seen[i], "number %d is missing from the sequence", i)
	}
}

// testIncidentNumbersArePerOrg proves the counter is scoped to an organization:
// a busy org must not push another org's first incident to #500.
func testIncidentNumbersArePerOrg(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	orgA, checkA := seedIncidentOrg(ctx, t, svc, "inc-num-org-a")
	orgB, checkB := seedIncidentOrg(ctx, t, svc, "inc-num-org-b")

	first := models.NewIncident(orgA, checkA, time.Now(), "a1")
	r.NoError(svc.CreateIncident(ctx, first))
	r.EqualValues(1, first.Number)

	second := models.NewIncident(orgA, checkA, time.Now(), "a2")
	r.NoError(svc.CreateIncident(ctx, second))
	r.EqualValues(2, second.Number)

	other := models.NewIncident(orgB, checkB, time.Now(), "b1")
	r.NoError(svc.CreateIncident(ctx, other))
	r.EqualValues(1, other.Number, "a fresh org starts at #1 regardless of other orgs")
}

// testGetIncidentByNumber proves the `#42` → incident resolution the typed
// Telegram commands run on.
func testGetIncidentByNumber(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	orgA, checkA := seedIncidentOrg(ctx, t, svc, "inc-num-lookup-a")
	orgB, _ := seedIncidentOrg(ctx, t, svc, "inc-num-lookup-b")

	incident := models.NewIncident(orgA, checkA, time.Now(), "lookup")
	r.NoError(svc.CreateIncident(ctx, incident))

	found, err := svc.GetIncidentByNumber(ctx, orgA, incident.Number)
	r.NoError(err)
	r.NotNil(found)
	r.Equal(incident.UID, found.UID)

	// Cross-org isolation: the same number in another org must not resolve to
	// this incident, or a Telegram chat linked to org B could ack org A's page.
	_, err = svc.GetIncidentByNumber(ctx, orgB, incident.Number)
	r.Error(err)
}

// testRealNumberCollisionIsClassified is the positive control behind the
// narrowed retry classifier.
//
// db.IsIncidentNumberCollision matches on DRIVER ERROR TEXT — Postgres names the
// index, SQLite names the columns — and the unit tests feed it strings a human
// typed. If either driver ever worded it differently, those tests would still
// pass while the real retry loop stopped retrying, and the parallel race test
// above would only notice on the unlucky run where a collision actually
// happened. So the violation is produced here by the REAL database, and the
// classifier is asked about the real error.
func testRealNumberCollisionIsClassified(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)
	orgUID, checkUID := seedIncidentOrg(ctx, t, svc, "inc-num-classify")

	first := models.NewIncident(orgUID, checkUID, time.Now(), "first")
	r.NoError(svc.CreateIncident(ctx, first))
	r.Positive(first.Number)

	// An EXPLICIT number skips the assignment loop, so this insert hits the
	// unique index head-on instead of being retried into a free slot.
	clash := models.NewIncident(orgUID, checkUID, time.Now(), "clash")
	clash.Number = first.Number

	err := svc.CreateIncident(ctx, clash)
	r.Error(err, "reusing a number must violate the unique index")
	r.True(db.IsUniqueViolation(err), "unexpected driver wording: %v", err)
	r.True(db.IsIncidentNumberCollision(err),
		"the retry classifier must recognize this engine's real wording: %v", err)
}
