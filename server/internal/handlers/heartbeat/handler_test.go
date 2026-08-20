package heartbeat_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/heartbeat"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// heartbeatHandlerSetup spins up an in-memory sqlite world plus a router
// wired to the real handler, mirroring checks' TestCreateCheckHandler pattern
// — this exercises the HTTP layer (header parsing, MaxBytesReader) that a
// service-level test can't reach.
type heartbeatHandlerSetup struct {
	dbSvc  *sqlite.Service
	router *httpx.Router
	org    *models.Organization
	check  *models.Check
}

func newHeartbeatHandlerSetup(t *testing.T) *heartbeatHandlerSetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := heartbeat.NewService(dbSvc, jobs, nil, nil)
	handler := heartbeat.NewHandler(svc, &config.Config{})

	org := models.NewOrganization("hb-handler-test", "Heartbeat Handler Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "cron-job", string(checkerdef.CheckTypeHeartbeat))
	check.Config["token"] = testToken
	r.NoError(dbSvc.CreateCheck(ctx, check))

	router := httpx.New()
	router.GET("/api/v1/heartbeat/:org/:identifier", handler.ReceiveHeartbeat)
	router.POST("/api/v1/heartbeat/:org/:identifier", handler.ReceiveHeartbeat)

	return &heartbeatHandlerSetup{dbSvc: dbSvc, router: router, org: org, check: check}
}

func (s *heartbeatHandlerSetup) url() string {
	return "/api/v1/heartbeat/" + s.org.Slug + "/" + s.check.UID
}

func (s *heartbeatHandlerSetup) lastOutput(t *testing.T) models.JSONMap {
	t.Helper()

	return s.lastResult(t).Output
}

func (s *heartbeatHandlerSetup) lastResult(t *testing.T) *models.Result {
	t.Helper()

	results, err := s.dbSvc.GetLastResultForChecks(t.Context(), s.org.UID, []string{s.check.UID})
	require.NoError(t, err)
	require.Contains(t, results, s.check.UID)

	return results[s.check.UID]
}

func TestReceiveHeartbeatHandlerQueryToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, s.url()+"?token="+testToken, nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
}

func TestReceiveHeartbeatHandlerHeaderToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, s.url(), nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
}

func TestReceiveHeartbeatHandlerHeaderWinsOverQuery(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	// Query carries a wrong token; header carries the real one. Header must win.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, s.url()+"?token=wrong-token", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
}

func TestReceiveHeartbeatHandlerMissingTokenReturns401(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, s.url(), nil)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	r.Equal(http.StatusUnauthorized, rec.Code, rec.Body.String())
}

func TestReceiveHeartbeatHandlerStructuredBodyNestsUnderData(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	body, err := json.Marshal(map[string]any{
		"message": "batch done",
		"runId":   "abc-123",
		"count":   3,
	})
	r.NoError(err)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, s.url()+"?token="+testToken, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	output := s.lastOutput(t)
	r.Equal("batch done", output["message"])
	data, ok := output["data"].(map[string]any)
	r.True(ok, "data must be a nested object")
	r.Equal("abc-123", data["runId"])
	r.InEpsilon(float64(3), data["count"], 0.0001)
}

func TestReceiveHeartbeatHandlerNoDataKeyWhenOnlyMessage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	body, err := json.Marshal(map[string]any{"message": "all good"})
	r.NoError(err)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, s.url()+"?token="+testToken, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	output := s.lastOutput(t)
	r.NotContains(output, "data")
}

func TestReceiveHeartbeatHandlerMalformedBodyToleratedUnderCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, s.url()+"?token="+testToken, strings.NewReader("{not valid json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String(), "malformed JSON under the cap must still record the ping")

	output := s.lastOutput(t)
	r.NotContains(output, "data")
	r.Equal("Heartbeat received", output["message"], "malformed body falls back to the default status message")
}

func TestReceiveHeartbeatHandlerOverCapBodyRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	// 8 KiB cap: build a body comfortably over it.
	huge := strings.Repeat("a", 9*1024)
	body, err := json.Marshal(map[string]any{"message": "hi", "blob": huge})
	r.NoError(err)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, s.url()+"?token="+testToken, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())

	var errResp map[string]string
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &errResp))
	r.Equal("VALIDATION_ERROR", errResp["code"])
}

// postHeartbeatBody POSTs body as the heartbeat JSON payload and returns the
// persisted result.
func (s *heartbeatHandlerSetup) postHeartbeatBody(t *testing.T, body map[string]any) *models.Result {
	t.Helper()
	r := require.New(t)

	encoded, err := json.Marshal(body)
	r.NoError(err)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, s.url()+"?token="+testToken, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	return s.lastResult(t)
}

func TestReceiveHeartbeatHandlerDurationMsConsumedFromBody(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	result := s.postHeartbeatBody(t, map[string]any{"message": "backup done", "durationMs": 42000})

	r.NotNil(result.Duration)
	r.InEpsilon(float32(42000), *result.Duration, 0.0001)
	r.NotContains(result.Output, "data", "a valid durationMs must be consumed, not duplicated under output.data")
}

func TestReceiveHeartbeatHandlerDurationMsAbsentKeepsDurationZero(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	result := s.postHeartbeatBody(t, map[string]any{"message": "no duration here"})

	r.NotNil(result.Duration)
	r.InDelta(float32(0), *result.Duration, 0.0001)
}

func TestReceiveHeartbeatHandlerDurationMsBoundaryAtCapAccepted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	// 604,800,000 ms == 7 days, the documented sanity cap; the boundary value
	// itself must be accepted.
	result := s.postHeartbeatBody(t, map[string]any{"durationMs": 604800000})

	r.NotNil(result.Duration)
	r.InEpsilon(float32(604800000), *result.Duration, 0.0001)
	r.NotContains(result.Output, "data")
}

func TestReceiveHeartbeatHandlerDurationMsOverCapIgnoredAndLeftInData(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	result := s.postHeartbeatBody(t, map[string]any{"durationMs": 604800001})

	r.NotNil(result.Duration)
	r.InDelta(float32(0), *result.Duration, 0.0001, "over-cap durationMs must not be stored as the duration")

	data, ok := result.Output["data"].(map[string]any)
	r.True(ok, "over-cap durationMs must still be visible to the caller under output.data")
	r.InEpsilon(float64(604800001), data["durationMs"], 0.0001)
}

func TestReceiveHeartbeatHandlerDurationMsNegativeIgnoredAndLeftInData(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	result := s.postHeartbeatBody(t, map[string]any{"durationMs": -5})

	r.NotNil(result.Duration)
	r.InDelta(float32(0), *result.Duration, 0.0001, "negative durationMs must not be stored as the duration")

	data, ok := result.Output["data"].(map[string]any)
	r.True(ok, "negative durationMs must still be visible to the caller under output.data")
	r.InEpsilon(float64(-5), data["durationMs"], 0.0001)
}

func TestReceiveHeartbeatHandlerDurationMsStringIgnoredAndLeftInData(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	result := s.postHeartbeatBody(t, map[string]any{"durationMs": "42000"})

	r.NotNil(result.Duration)
	r.InDelta(float32(0), *result.Duration, 0.0001, "non-numeric durationMs must not be stored as the duration")

	data, ok := result.Output["data"].(map[string]any)
	r.True(ok, "string durationMs must still be visible to the caller under output.data")
	r.Equal("42000", data["durationMs"])
}

func TestReceiveHeartbeatHandlerDurationMsAppliesToRunningStatus(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	body, err := json.Marshal(map[string]any{"durationMs": 1500})
	r.NoError(err)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, s.url()+"?token="+testToken+"&status=running", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	result := s.lastResult(t)
	r.NotNil(result.Duration)
	r.InEpsilon(float32(1500), *result.Duration, 0.0001, "durationMs applies to every accepted status, including running")
}

func TestReceiveHeartbeatHandlerDurationMsNaNIgnoredWithoutFailingPing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatHandlerSetup(t)

	// NaN/Infinity have no valid JSON literal, so a caller can only send them
	// as raw (technically malformed) body text. That already falls under the
	// existing malformed-JSON leniency path: the ping must still succeed and
	// duration must stay at 0 — see extractHeartbeatDuration's own NaN/Inf
	// guard for the defensive check exercised when this function is called
	// directly on a decoded map containing a non-finite float64.
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, s.url()+"?token="+testToken, strings.NewReader(`{"durationMs": NaN}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String(), "a bad durationMs must never fail the ping")

	result := s.lastResult(t)
	r.NotNil(result.Duration)
	r.InDelta(float32(0), *result.Duration, 0.0001)
}
