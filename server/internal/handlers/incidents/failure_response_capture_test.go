package incidents_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// bodyMarker is a string that appears nowhere else, so "the capture reached
// surface X" and "it did not" are both decidable by a substring search.
const bodyMarker = "acme-origin-stack-trace-marker"

// capturedDownResult is a failing result carrying an opt-in failure capture,
// the shape checkhttp produces for a 503 with a text body.
func capturedDownResult(orgUID, checkUID, errMsg string) *models.Result {
	result := downResult(orgUID, checkUID, errMsg)
	result.Diagnostics = &checkerdef.Diagnostics{
		FailureResponse: &checkerdef.FailureResponse{
			URL:           "https://acme.com/health",
			StatusLine:    "HTTP/1.1 503 Service Unavailable",
			StatusCode:    503,
			Headers:       map[string]string{"Server": "acme-lb", "Set-Cookie": "[redacted]"},
			Body:          "<html><body>" + bodyMarker + "</body></html>",
			ContentLength: 64,
			ContentType:   "text/html; charset=utf-8",
			BodyBytes:     64,
			BodySHA256:    strings.Repeat("ab", 32),
			CapturedAt:    time.Now().UTC(),
			RemoteAddr:    "203.0.113.10:443",
		},
	}

	return result
}

// TestCreateIncidentPersistsFailureResponse is the headline: an incident
// opened by a captured failure keeps the capture under details.failureResponse.
func TestCreateIncidentPersistsFailureResponse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	result := capturedDownResult(s.org.UID, s.check.UID, "unexpected status code 503")
	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	capture, ok := inc.Details["failureResponse"].(map[string]any)
	r.True(ok, "failureResponse must decode as an object")
	r.Equal("HTTP/1.1 503 Service Unavailable", capture["statusLine"])
	r.EqualValues(503, capture["statusCode"])
	r.Contains(capture["body"], bodyMarker)
	r.Equal("https://acme.com/health", capture["url"])
	r.Equal("203.0.113.10:443", capture["remoteAddr"])
	// The region is stamped server-side from the result row, not by the probe.
	r.Equal("eu", capture["region"])

	headers, ok := capture["headers"].(map[string]any)
	r.True(ok)
	r.Equal("[redacted]", headers["Set-Cookie"])

	// The existing snapshot keys are untouched.
	r.Equal("unexpected status code 503", inc.Details["failure_reason"])
	r.Contains(inc.Details, "first_result")
}

// TestCreateIncidentWithoutCaptureOmitsFailureResponse pins the default: a
// check that did not opt in leaves no failureResponse key at all.
func TestCreateIncidentWithoutCaptureOmitsFailureResponse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	result := downResult(s.org.UID, s.check.UID, "connection refused")
	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	// Positive control: details were written, they are just capture-free.
	r.Equal("connection refused", inc.Details["failure_reason"])
	r.NotContains(inc.Details, "failureResponse")
}

// TestReopenIncidentOverwritesFailureResponse pins the reopen rule: the
// capture always describes the CURRENT onset, so a relapse replaces it.
func TestReopenIncidentOverwritesFailureResponse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	first := capturedDownResult(s.org.UID, s.check.UID, "original 503")
	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, first))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	resolveIncidentForReopen(t, s, inc.UID)

	relapse := capturedDownResult(s.org.UID, s.check.UID, "relapse 502")
	relapse.Diagnostics.FailureResponse.StatusCode = 502
	relapse.Diagnostics.FailureResponse.StatusLine = "HTTP/1.1 502 Bad Gateway"
	relapse.Diagnostics.FailureResponse.Body = "relapse-body-marker"

	r.NoError(s.svc.CreateOrReopenIncidentForTest(ctx, s.check, relapse))

	reopened, err := s.dbSvc.GetIncident(ctx, s.org.UID, inc.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateActive, reopened.State, "must have reopened the same incident")

	capture, ok := reopened.Details["failureResponse"].(map[string]any)
	r.True(ok)
	r.EqualValues(502, capture["statusCode"])
	r.Equal("relapse-body-marker", capture["body"])
	r.NotContains(capture["body"], bodyMarker, "the onset capture must be the relapse's, not the original's")

	// The original narrative keys are still preserved, as before.
	r.Equal("original 503", reopened.Details["failure_reason"])
}

// TestReopenWithoutCaptureClearsStaleFailureResponse: when the relapse carries
// no capture (opted out since, or a timeout that never got a response), the
// stale capture is DROPPED rather than left describing a different outage.
func TestReopenWithoutCaptureClearsStaleFailureResponse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	first := capturedDownResult(s.org.UID, s.check.UID, "original 503")
	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, first))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	// Positive control: the capture really was there before the relapse.
	r.Contains(inc.Details, "failureResponse")

	resolveIncidentForReopen(t, s, inc.UID)

	relapse := downResult(s.org.UID, s.check.UID, "request timed out")
	r.NoError(s.svc.CreateOrReopenIncidentForTest(ctx, s.check, relapse))

	reopened, err := s.dbSvc.GetIncident(ctx, s.org.UID, inc.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateActive, reopened.State)
	r.NotContains(reopened.Details, "failureResponse",
		"a stale capture must never outlive the onset it describes")
	// And the rest of the relapse bookkeeping still happened.
	r.Contains(reopened.Details, "last_failure")
}

// resolveIncidentForReopen resolves an incident "just now", i.e. inside the
// reopen cooldown window, so the next failure reattaches to it.
func resolveIncidentForReopen(t *testing.T, s *failureSnapshotSetup, incidentUID string) {
	t.Helper()

	resolvedState := models.IncidentStateResolved
	resolvedAt := time.Now()
	require.NoError(t, s.dbSvc.UpdateIncident(t.Context(), incidentUID, &models.IncidentUpdate{
		State:      &resolvedState,
		ResolvedAt: &resolvedAt,
	}))
}

// TestNonTransitionFailureDropsCapture: a failure that neither opens nor
// reopens an incident (confirmation threshold not crossed) must leave the
// capture on the floor — it only ever existed in memory and on the wire.
func TestNonTransitionFailureDropsCapture(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newValidatingSetup(t, 3600) // one hour of confirmation: nothing opens

	result := capturedDownResult(s.org.UID, s.check.UID, "unexpected status code 503")
	r.NoError(s.dbSvc.CreateResult(ctx, result))
	r.NoError(s.svc.ProcessCheckResult(ctx, s.check, result))

	r.False(s.hasActiveIncident(t), "confirmation not elapsed — no incident may exist")
	r.NotContains(allIncidentDetailsJSON(t, s), bodyMarker,
		"a failure that opened nothing must leave the capture on the floor")
	// Positive control on the results query itself: the row IS there and its
	// output column IS readable — so "the marker is absent" means absent, not
	// "the query found nothing".
	persistedOutputs := allResultOutputJSON(t, s)
	r.Contains(persistedOutputs, "unexpected status code 503")
	r.NotContains(persistedOutputs, bodyMarker,
		"the capture must never reach the results table")
	r.NotContains(persistedOutputs, "failureResponse")

	// Positive control: the very same result DOES persist its capture once the
	// failure actually opens an incident.
	opening := newValidatingSetup(t, 0)
	opened := capturedDownResult(opening.org.UID, opening.check.UID, "unexpected status code 503")
	r.NoError(opening.dbSvc.CreateResult(ctx, opened))
	r.NoError(opening.svc.ProcessCheckResult(ctx, opening.check, opened))
	r.True(opening.hasActiveIncident(t))
	r.Contains(allIncidentDetailsJSON(t, opening), bodyMarker)
}

// allIncidentDetailsJSON returns every incident row's `details` column for the
// setup's org, concatenated — so a test can assert a marker reached (or never
// reached) ANY incident, not just the one it happens to look up.
func allIncidentDetailsJSON(t *testing.T, s *validatingSetup) string {
	t.Helper()

	var details []string

	err := s.dbSvc.DB().NewSelect().
		Table("incidents").
		Column("details").
		Where("organization_uid = ?", s.org.UID).
		Scan(t.Context(), &details)
	require.NoError(t, err)

	return strings.Join(details, "\n")
}

// allResultOutputJSON returns every persisted raw result's `output` column. It
// is the direct proof that the capture is invisible to the results table.
func allResultOutputJSON(t *testing.T, s *validatingSetup) string {
	t.Helper()

	var outputs []string

	err := s.dbSvc.DB().NewSelect().
		Table("results").
		ColumnExpr("COALESCE(output, '')").
		Where("organization_uid = ?", s.org.UID).
		Scan(t.Context(), &outputs)
	require.NoError(t, err)

	return strings.Join(outputs, "\n")
}

// TestSuccessNeverPersistsCapture: a successful result never contributes a
// capture, even if one somehow rode along on the wire.
func TestSuccessNeverPersistsCapture(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newValidatingSetup(t, 0) // open immediately on failure

	// Open an incident with a captured failure (positive control).
	failing := capturedDownResult(s.org.UID, s.check.UID, "unexpected status code 503")
	r.NoError(s.dbSvc.CreateResult(ctx, failing))
	r.NoError(s.svc.ProcessCheckResult(ctx, s.check, failing))
	r.True(s.hasActiveIncident(t))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.Contains(inc.Details, "failureResponse")

	// A success carrying a (nonsensical) capture must not rewrite details.
	status := int(models.ResultStatusUp)
	up := &models.Result{
		UID:             uuid.New().String(),
		OrganizationUID: s.org.UID,
		CheckUID:        s.check.UID,
		PeriodType:      models.PeriodTypeRaw,
		PeriodStart:     time.Now(),
		Status:          &status,
		Output:          models.JSONMap{},
		Diagnostics: &checkerdef.Diagnostics{
			FailureResponse: &checkerdef.FailureResponse{Body: "should-never-be-persisted"},
		},
	}
	r.NoError(s.dbSvc.CreateResult(ctx, up))
	r.NoError(s.svc.ProcessCheckResult(ctx, s.check, up))

	after, err := s.dbSvc.GetIncident(ctx, s.org.UID, inc.UID)
	r.NoError(err)

	raw, err := json.Marshal(after.Details)
	r.NoError(err)
	r.NotContains(string(raw), "should-never-be-persisted")
	// The onset capture is untouched.
	r.Contains(string(raw), bodyMarker)
}

// TestFailureDetailsCaptureIsBoundedBySnapshotCapIndependently guards the size
// story: the capture lives OUTSIDE first_result, so the snapshot's output cap
// neither truncates it nor is inflated by it.
func TestFailureDetailsCaptureIsBoundedIndependently(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	result := capturedDownResult("org", "check", "unexpected status code 503")
	details := incidents.FailureDetailsForTest(result)

	first, ok := details["first_result"].(models.JSONMap)
	r.True(ok)

	firstRaw, err := json.Marshal(first)
	r.NoError(err)
	r.NotContains(string(firstRaw), bodyMarker,
		"the capture must never be folded into the result snapshot's output map")

	capture, ok := details["failureResponse"].(*checkerdef.FailureResponse)
	r.True(ok)
	r.Contains(capture.Body, bodyMarker)
}
