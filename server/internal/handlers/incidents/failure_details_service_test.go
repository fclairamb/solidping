package incidents_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// failureSnapshotSetup spins up an in-memory sqlite + incidents service, the
// same shape as resolveSetup in resolve_test.go, but without a pre-created
// incident — these tests build the incident themselves via the CreateXForTest
// export hooks so they exercise the real open/reopen/group code paths.
type failureSnapshotSetup struct {
	svc   *incidents.Service
	dbSvc *sqlite.Service
	org   *models.Organization
	check *models.Check
}

func newFailureSnapshotSetup(t *testing.T) *failureSnapshotSetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	org := models.NewOrganization("failure-snap", "Failure Snapshot Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	return &failureSnapshotSetup{svc: svc, dbSvc: dbSvc, org: org, check: check}
}

// downResult builds a raw "down" result carrying the given error string in
// Output[checkerdef.OutputKeyError], the same shape a real checker produces.
// The result is never persisted to the results table, only snapshotted onto
// the incident, so its UID only needs to be unique within the test.
func downResult(orgUID, checkUID, errMsg string) *models.Result {
	status := int(models.ResultStatusDown)
	region := "eu"
	duration := float32(0.75)

	return &models.Result{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		CheckUID:        checkUID,
		PeriodType:      models.PeriodTypeRaw,
		PeriodStart:     time.Now(),
		Region:          &region,
		Status:          &status,
		Duration:        &duration,
		Output:          models.JSONMap{checkerdef.OutputKeyError: errMsg},
	}
}

// TestCreateIncident_WritesFailureDetails pins the headline fix: opening an
// incident must snapshot the triggering result's failure reason and a
// first_result object into incident.Details.
func TestCreateIncident_WritesFailureDetails(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	result := downResult(s.org.UID, s.check.UID, "connection refused")

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.NotNil(inc.Details)
	r.Equal("connection refused", inc.Details["failure_reason"])

	first, ok := inc.Details["first_result"].(map[string]any)
	r.True(ok, "first_result must decode as an object")
	r.Equal(result.UID, first["resultUid"])
	r.Equal("DOWN", first["status"])
	r.Equal("eu", first["region"])

	output, ok := first["output"].(map[string]any)
	r.True(ok)
	r.Equal("connection refused", output[checkerdef.OutputKeyError])

	_, hasLastFailure := inc.Details["last_failure"]
	r.False(hasLastFailure, "no relapse yet — last_failure must be absent")
}

// TestCreateIncident_NoOutputErrorFallsBackToStatus pins the fallback: when
// the checker didn't set an error string, failure_reason derives from status.
func TestCreateIncident_NoOutputErrorFallsBackToStatus(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	status := int(models.ResultStatusTimeout)
	result := &models.Result{
		UID:             uuid.New().String(),
		OrganizationUID: s.org.UID,
		CheckUID:        s.check.UID,
		PeriodType:      models.PeriodTypeRaw,
		PeriodStart:     time.Now(),
		Status:          &status,
		Output:          models.JSONMap{},
	}

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.Equal("timeout", inc.Details["failure_reason"])
}

// TestReopenIncident_AddsLastFailurePreservesFirstResult is the relapse
// contract: reopening a resolved incident must add a last_failure snapshot
// of the new failing result while leaving the original first_result and
// failure_reason exactly as they were at first open.
func TestReopenIncident_AddsLastFailurePreservesFirstResult(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	firstFailure := downResult(s.org.UID, s.check.UID, "original cause: DNS failure")
	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, firstFailure))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	// Resolve it, recently enough to be within the reopen cooldown window
	// (default check period 1m -> 5m cooldown, clamped to [2m,30m] => 5m).
	resolvedState := models.IncidentStateResolved
	resolvedAt := time.Now()
	r.NoError(s.dbSvc.UpdateIncident(ctx, inc.UID, &models.IncidentUpdate{
		State:      &resolvedState,
		ResolvedAt: &resolvedAt,
	}))

	relapseResult := downResult(s.org.UID, s.check.UID, "relapse cause: TLS handshake failed")
	r.NoError(s.svc.CreateOrReopenIncidentForTest(ctx, s.check, relapseResult))

	reopened, err := s.dbSvc.GetIncident(ctx, s.org.UID, inc.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateActive, reopened.State, "must have reopened the same incident, not created a new one")

	r.Equal("original cause: DNS failure", reopened.Details["failure_reason"],
		"failure_reason must be untouched by the reopen")

	first, ok := reopened.Details["first_result"].(map[string]any)
	r.True(ok)
	r.Equal(firstFailure.UID, first["resultUid"], "first_result must still point at the original failing result")

	last, ok := reopened.Details["last_failure"].(map[string]any)
	r.True(ok, "last_failure must be present after a relapse")
	r.Equal(relapseResult.UID, last["resultUid"])
	output, ok := last["output"].(map[string]any)
	r.True(ok)
	r.Equal("relapse cause: TLS handshake failed", output[checkerdef.OutputKeyError])
}

// TestCreateGroupIncident_WritesFailureDetails pins the group-create path:
// the triggering member's failure is the recorded cause, same shape as the
// per-check path.
func TestCreateGroupIncident_WritesFailureDetails(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	group := models.NewCheckGroup(s.org.UID, "API Group", "api-group")
	r.NoError(s.dbSvc.CreateCheckGroup(ctx, group))

	s.check.CheckGroupUID = &group.UID
	r.NoError(s.dbSvc.UpdateCheck(ctx, s.check.UID, &models.CheckUpdate{CheckGroupUID: &group.UID}))

	result := downResult(s.org.UID, s.check.UID, "group member unreachable")

	r.NoError(s.svc.CreateGroupIncidentForTest(ctx, s.check, result))

	inc, err := s.dbSvc.FindActiveIncidentByGroupUID(ctx, group.UID)
	r.NoError(err)
	r.Equal("group member unreachable", inc.Details["failure_reason"])

	first, ok := inc.Details["first_result"].(map[string]any)
	r.True(ok)
	r.Equal(result.UID, first["resultUid"])
}

// TestReopenGroupIncident_AddsLastFailurePreservesFirstResult mirrors the
// per-check reopen test for the group path.
func TestReopenGroupIncident_AddsLastFailurePreservesFirstResult(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	group := models.NewCheckGroup(s.org.UID, "API Group", "api-group")
	r.NoError(s.dbSvc.CreateCheckGroup(ctx, group))
	s.check.CheckGroupUID = &group.UID
	r.NoError(s.dbSvc.UpdateCheck(ctx, s.check.UID, &models.CheckUpdate{CheckGroupUID: &group.UID}))

	firstFailure := downResult(s.org.UID, s.check.UID, "original group cause")
	r.NoError(s.svc.CreateGroupIncidentForTest(ctx, s.check, firstFailure))

	inc, err := s.dbSvc.FindActiveIncidentByGroupUID(ctx, group.UID)
	r.NoError(err)

	resolvedState := models.IncidentStateResolved
	resolvedAt := time.Now()
	r.NoError(s.dbSvc.UpdateIncident(ctx, inc.UID, &models.IncidentUpdate{
		State:      &resolvedState,
		ResolvedAt: &resolvedAt,
	}))

	relapseResult := downResult(s.org.UID, s.check.UID, "relapse group cause")
	r.NoError(s.svc.CreateOrReopenGroupIncidentForTest(ctx, s.check, relapseResult))

	reopened, err := s.dbSvc.GetIncident(ctx, s.org.UID, inc.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateActive, reopened.State)

	r.Equal("original group cause", reopened.Details["failure_reason"])
	last, ok := reopened.Details["last_failure"].(map[string]any)
	r.True(ok)
	r.Equal(relapseResult.UID, last["resultUid"])
}

// TestCreateIncident_SizeCapEndToEnd pins the size cap through the real
// create path: an oversized Output must not blow up the stored Details, and
// the error string must survive.
func TestCreateIncident_SizeCapEndToEnd(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	status := int(models.ResultStatusDown)
	errMsg := "concise error"
	result := &models.Result{
		UID:             uuid.New().String(),
		OrganizationUID: s.org.UID,
		CheckUID:        s.check.UID,
		PeriodType:      models.PeriodTypeRaw,
		PeriodStart:     time.Now(),
		Status:          &status,
		Output: models.JSONMap{
			checkerdef.OutputKeyError: errMsg,
			"response_body":           strings.Repeat("a", 30*1024),
		},
	}

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	first := inc.Details["first_result"].(map[string]any) //nolint:forcetypeassert
	output := first["output"].(map[string]any)            //nolint:forcetypeassert
	r.Equal(errMsg, output[checkerdef.OutputKeyError], "error must survive the size cap")

	if body, ok := output["response_body"].(string); ok {
		r.Less(len(body), 30*1024, "oversized field must have been truncated, not kept whole")
	}
}
