package checkworker

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// getCheckCountingDB wraps a db.Service and counts GetCheck calls so the
// processIncidents tests can assert the claim-time check attachment removes the
// per-result GetCheck round-trip.
type getCheckCountingDB struct {
	db.Service
	getCheckCalls atomic.Int64
}

func (c *getCheckCountingDB) GetCheck(ctx context.Context, orgUID, checkUID string) (*models.Check, error) {
	c.getCheckCalls.Add(1)

	return c.Service.GetCheck(ctx, orgUID, checkUID)
}

func newProcessIncidentsWorker(t *testing.T) (*CheckWorker, *getCheckCountingDB, *sqlite.Service, context.Context) {
	t.Helper()
	ctx := context.Background()

	sqliteSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, sqliteSvc.Initialize(ctx))
	t.Cleanup(func() { _ = sqliteSvc.Close() })

	counting := &getCheckCountingDB{Service: sqliteSvc}

	cfg := &config.Config{}
	svcList := services.NewRegistry()
	svcList.CheckJobs = checkjobsvc.NewService(sqliteSvc.DB())
	eventNotifier := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = eventNotifier.Close() })
	svcList.EventNotifier = eventNotifier

	// Build the worker against the counting db so both the worker and its
	// internally-constructed incident service share the same call counter.
	runner := NewCheckWorker(counting, cfg, svcList, svcList.CheckJobs)

	return runner, counting, sqliteSvc, ctx
}

// TestProcessIncidentsUsesAttachedCheck pins acceptance criterion 2: when the
// CheckJob carries an attached Check, processIncidents must not call GetCheck.
func TestProcessIncidentsUsesAttachedCheck(t *testing.T) {
	t.Parallel()
	runner, counting, dbSvc, ctx := newProcessIncidentsWorker(t)

	org := models.NewOrganization("proc-inc-attached", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusUp
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	job := &models.CheckJob{
		UID:             "job-attached",
		OrganizationUID: org.UID,
		CheckUID:        check.UID,
		Check:           check, // attached at claim time
	}

	result := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0)
	require.NoError(t, dbSvc.CreateResult(ctx, result))

	runner.processIncidents(ctx, job, result)

	require.Equal(t, int64(0), counting.getCheckCalls.Load(),
		"processIncidents must not call GetCheck when the job has an attached check")
}

// TestProcessIncidentsFallsBackToGetCheck pins the fallback path: when the
// attached check is nil, processIncidents fetches it via GetCheck exactly once
// and still processes the result.
func TestProcessIncidentsFallsBackToGetCheck(t *testing.T) {
	t.Parallel()
	runner, counting, dbSvc, ctx := newProcessIncidentsWorker(t)

	org := models.NewOrganization("proc-inc-fallback", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusUp
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	job := &models.CheckJob{
		UID:             "job-fallback",
		OrganizationUID: org.UID,
		CheckUID:        check.UID,
		Check:           nil, // not attached -> fallback fetch
	}

	result := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0)
	require.NoError(t, dbSvc.CreateResult(ctx, result))

	runner.processIncidents(ctx, job, result)

	require.Equal(t, int64(1), counting.getCheckCalls.Load(),
		"processIncidents must fall back to a single GetCheck when no check is attached")
}
