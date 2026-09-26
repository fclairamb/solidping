package checkscreenshots_test

import (
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checkscreenshots"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portCaptureConcurrency is this file's embedded-Postgres port, distinct from
// every other *_postgres test in the repo.
const portCaptureConcurrency = 15613

// exerciseConcurrentCaptures fires "Capture now" from many goroutines at once,
// each on a DIFFERENT check of one org, so only the org window is contended:
// exactly CaptureOrgLimit must be admitted and every other request refused by
// the org cap. A read-then-write limiter admits them all (every goroutine reads
// the same count before any writes it back), which is the defect this pins.
func exerciseConcurrentCaptures(t *testing.T, dbSvc db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	org := models.NewOrganization("burst", "Burst")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	const requests = checkscreenshots.CaptureOrgLimit + 15

	checks := make([]*models.Check, 0, requests)

	for i := range requests {
		check := models.NewCheck(org.UID, "burst-"+strconv.Itoa(i), "browser")
		r.NoError(dbSvc.CreateCheck(ctx, check))
		checks = append(checks, check)
	}

	svc := checkscreenshots.NewService(dbSvc, nil, nil, nil)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
		refused  int
		failures []error
	)

	start := make(chan struct{})

	for _, check := range checks {
		wg.Add(1)

		go func(checkUID string) {
			defer wg.Done()

			<-start

			_, err := svc.CaptureNow(ctx, org.Slug, checkUID)

			mu.Lock()
			defer mu.Unlock()

			var limited *checkscreenshots.RateLimitedError

			switch {
			case err == nil:
				admitted++
			case errors.As(err, &limited) && limited.Scope == "organization":
				refused++
			default:
				failures = append(failures, err)
			}
		}(check.UID)
	}

	close(start)
	wg.Wait()

	r.Empty(failures)
	r.Equal(checkscreenshots.CaptureOrgLimit, admitted, "exactly the org cap is admitted")
	r.Equal(requests-checkscreenshots.CaptureOrgLimit, refused, "every other request is refused by the org cap")

	// The counter agrees with what was admitted.
	entry, err := dbSvc.GetStateEntry(ctx, &org.UID, "capture-now.org")
	r.NoError(err)
	r.NotNil(entry)
	r.InDelta(float64(checkscreenshots.CaptureOrgLimit), (*entry.Value)["count"], 0)
}

// TestConcurrentCapturesAdmitExactlyTheOrgCap runs it on SQLite.
func TestConcurrentCapturesAdmitExactlyTheOrgCap(t *testing.T) {
	t.Parallel()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	exerciseConcurrentCaptures(t, dbSvc)
}

// TestConcurrentCapturesAdmitExactlyTheOrgCap_Postgres runs it on Postgres,
// where the goroutines really do run their transactions in parallel.
func TestConcurrentCapturesAdmitExactlyTheOrgCap_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portCaptureConcurrency, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	exerciseConcurrentCaptures(t, dbSvc)
}
