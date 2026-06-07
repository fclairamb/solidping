package incidents_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// countingDBService wraps a real db.Service and counts how many times
// ListMaintenanceWindowsForCheck is called, so the TTL-cache test can assert
// the cache actually suppresses redundant fetches. Everything else delegates.
type countingDBService struct {
	db.Service
	mwFetches atomic.Int64
}

func (c *countingDBService) ListMaintenanceWindowsForCheck(
	ctx context.Context, checkUID string,
) ([]*models.MaintenanceWindow, error) {
	c.mwFetches.Add(1)

	return c.Service.ListMaintenanceWindowsForCheck(ctx, checkUID)
}

// TestMaintenanceWindowTTLCache pins acceptance criterion 3: the underlying
// ListMaintenanceWindowsForCheck DB call fires at most once per TTL per
// check-uid regardless of result volume, while models.IsActiveAt is still
// evaluated on every result so a window becoming active mid-TTL is detected
// without a re-fetch.
func TestMaintenanceWindowTTLCache(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	counting := &countingDBService{Service: dbSvc}

	// Start the fake clock well before the window so it is inactive at t0.
	start := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(start)

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier())
	svc := incidents.NewService(counting, jobs, fakeClock)

	org := models.NewOrganization("mw-cache-test", "MW Cache Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusUp
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// A non-recurring window that becomes active 30s after t0 (inside the 60s
	// TTL) and lasts 1h. At t0 it is NOT active.
	windowStart := start.Add(30 * time.Second)
	window := models.NewMaintenanceWindow(org.UID, "deploy", windowStart, windowStart.Add(time.Hour))
	r.NoError(dbSvc.CreateMaintenanceWindow(ctx, window))
	r.NoError(dbSvc.SetMaintenanceWindowChecks(ctx, window.UID, []string{check.UID}, nil))

	submit := func() {
		result := models.NewResult(org.UID, check.UID, models.ResultStatusDown, 0)
		r.NoError(dbSvc.CreateResult(ctx, result))
		r.NoError(svc.ProcessCheckResult(ctx, check, result))
	}

	// First result at t0: window inactive -> incident processing runs (the
	// failure is not skipped for maintenance); one fetch populates the cache.
	submit()
	r.Equal(int64(1), counting.mwFetches.Load(), "first result must fetch windows once")

	// Several more results at t0: all served from cache, still inactive.
	submit()
	submit()
	r.Equal(int64(1), counting.mwFetches.Load(),
		"results within TTL must not re-fetch maintenance windows")

	// Advance 40s (past window start, still inside the 60s TTL). The window is
	// now active and must be detected WITHOUT a re-fetch: count stays at 1.
	fakeClock.Advance(40 * time.Second)
	r.True(window.StartAt.Before(fakeClock.Now()), "window has started on the fake clock")
	r.Less(fakeClock.Now().Sub(start), 60*time.Second, "still inside the TTL")

	r.NoError(svc.ProcessCheckResult(ctx,
		check, models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0)))
	r.Equal(int64(1), counting.mwFetches.Load(),
		"a window becoming active mid-TTL must be detected from the cache, no re-fetch")

	// Advance past the TTL boundary (total 70s > 60s TTL). Next result must
	// trigger exactly one re-fetch.
	fakeClock.Advance(30 * time.Second)
	r.GreaterOrEqual(fakeClock.Now().Sub(start), 60*time.Second, "past the TTL boundary")

	r.NoError(svc.ProcessCheckResult(ctx,
		check, models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0)))
	r.Equal(int64(2), counting.mwFetches.Load(),
		"a result past the TTL must re-fetch exactly once")
}
