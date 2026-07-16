package backend_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// getCheckCountingDB wraps a db.Service and counts GetCheck calls so the
// incident-processing tests can assert the claim-time check attachment removes
// the per-result GetCheck round-trip (behavior moved here from CheckWorker by
// the WorkerBackend refactor, spec 2026-07-16-02).
type getCheckCountingDB struct {
	db.Service
	getCheckCalls atomic.Int64
}

func (c *getCheckCountingDB) GetCheck(ctx context.Context, orgUID, checkUID string) (*models.Check, error) {
	c.getCheckCalls.Add(1)

	return c.Service.GetCheck(ctx, orgUID, checkUID)
}

func newDirectBackend(t *testing.T) (*backend.DirectBackend, *getCheckCountingDB, *sqlite.Service, context.Context) {
	t.Helper()
	ctx := context.Background()

	sqliteSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, sqliteSvc.Initialize(ctx))
	t.Cleanup(func() { _ = sqliteSvc.Close() })

	counting := &getCheckCountingDB{Service: sqliteSvc}

	eventNotifier := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = eventNotifier.Close() })

	checkJobSvc := checkjobsvc.NewService(sqliteSvc.DB())
	incidentSvc := incidents.NewService(counting, nil, clock.Real{}, nil)

	return backend.NewDirectBackend(counting, checkJobSvc, incidentSvc, eventNotifier), counting, sqliteSvc, ctx
}

// submitReq builds a minimal successful-result submit request.
func submitReq() *backend.SubmitResultRequest {
	return &backend.SubmitResultRequest{
		Status:          int(models.ResultStatusUp),
		Duration:        1,
		NextScheduledAt: time.Now().Add(time.Minute),
	}
}

// registerWorker creates a workers row (results.worker_uid is a foreign key).
func registerWorker(t *testing.T, ctx context.Context, dbSvc *sqlite.Service, slug string) string {
	t.Helper()

	registered, err := dbSvc.RegisterOrUpdateWorker(ctx, models.NewWorker(slug, slug))
	require.NoError(t, err)

	return registered.UID
}

// TestSubmitResultUsesAttachedCheck pins the claim-time attachment contract:
// when the CheckJob carries an attached Check, incident processing must not
// call GetCheck.
func TestSubmitResultUsesAttachedCheck(t *testing.T) {
	t.Parallel()
	be, counting, dbSvc, ctx := newDirectBackend(t)

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

	workerUID := registerWorker(t, ctx, dbSvc, "wk-attached")

	// The lease release may fail (no check_jobs row) — irrelevant here; the
	// assertion is about the GetCheck round-trip on the incident path.
	_ = be.SubmitResult(ctx, job, workerUID, submitReq())

	require.Equal(t, int64(0), counting.getCheckCalls.Load(),
		"incident processing must not call GetCheck when the job has an attached check")
}

// TestSubmitResultFallsBackToGetCheck pins the fallback path: when the attached
// check is nil, incident processing fetches it via GetCheck exactly once.
func TestSubmitResultFallsBackToGetCheck(t *testing.T) {
	t.Parallel()
	be, counting, dbSvc, ctx := newDirectBackend(t)

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

	workerUID := registerWorker(t, ctx, dbSvc, "wk-fallback")

	_ = be.SubmitResult(ctx, job, workerUID, submitReq())

	require.Equal(t, int64(1), counting.getCheckCalls.Load(),
		"incident processing must fall back to a single GetCheck when no check is attached")
}

// TestSubmitResultWritesResultRow verifies the result row lands with the
// submitted fields (the DirectBackend keeps the pre-refactor write behavior).
func TestSubmitResultWritesResultRow(t *testing.T) {
	t.Parallel()
	be, _, dbSvc, ctx := newDirectBackend(t)

	org := models.NewOrganization("submit-writes", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	job := &models.CheckJob{
		UID:             "job-writes",
		OrganizationUID: org.UID,
		CheckUID:        check.UID,
		Check:           check,
	}

	region := "eu"
	req := submitReq()
	req.Region = &region
	req.Output = map[string]any{"message": "ok"}

	workerUID := registerWorker(t, ctx, dbSvc, "wk-writes")

	_ = be.SubmitResult(ctx, job, workerUID, req)

	results, err := dbSvc.GetLastResultForChecks(ctx, org.UID, []string{check.UID})
	require.NoError(t, err)
	got, ok := results[check.UID]
	require.True(t, ok, "a result row must exist for the check")
	require.NotNil(t, got.Status)
	require.Equal(t, int(models.ResultStatusUp), *got.Status)
	require.NotNil(t, got.Region)
	require.Equal(t, "eu", *got.Region)
}
