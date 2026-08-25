package importers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// convertHarness wires the convert endpoint over an in-memory database, the way
// server.go wires it in production (same admin route group, same checks service).
type convertHarness struct {
	router *httpx.Router
	dbSvc  db.Service
	org    *models.Organization
}

// newConvertHarness builds the harness. betterStackBaseURL, when non-empty,
// points the Better Stack converter at a fake API.
func newConvertHarness(t *testing.T, betterStackBaseURL string) *convertHarness {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("import-org", "Import Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	checksSvc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), creds, entSvc)

	handler := importers.NewHandler(checksSvc, &config.Config{})
	if betterStackBaseURL != "" {
		handler = handler.WithBetterStackBaseURL(betterStackBaseURL)
	}

	router := httpx.New()
	handler.RegisterRoutes(router.NewGroup("/api/v1/orgs/:org/checks"))

	return &convertHarness{router: router, dbSvc: dbSvc, org: org}
}

// post issues one convert request and returns the recorder.
func (h *convertHarness) post(t *testing.T, source string, dryRun bool, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	url := "/api/v1/orgs/" + h.org.Slug + "/checks/import/convert?source=" + source
	if dryRun {
		url += "&dryRun=true"
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	return rec
}

// decode unmarshals a successful convert response.
func decodeConvert(t *testing.T, rec *httptest.ResponseRecorder) importers.ConvertResult {
	t.Helper()
	r := require.New(t)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var result importers.ConvertResult
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &result))

	return result
}

// countChecks returns the number of checks currently in the org.
func (h *convertHarness) countChecks(t *testing.T) int {
	t.Helper()

	list, _, err := h.dbSvc.ListChecks(t.Context(), h.org.UID, &models.ListChecksFilter{})
	require.NoError(t, err)

	return len(list)
}

// runSourceRoundTrip exercises dry-run → apply → idempotent re-apply for one source.
func runSourceRoundTrip(t *testing.T, harness *convertHarness, source string, body []byte) importers.ConvertResult {
	t.Helper()
	r := require.New(t)

	// 1. Dry run previews everything and mutates nothing.
	preview := decodeConvert(t, harness.post(t, source, true, body))
	r.True(preview.DryRun)
	r.Equal(source, preview.Source)
	r.Positive(preview.Converted)
	r.Equal(preview.Converted, preview.Created)
	r.Equal(0, harness.countChecks(t), "dry run must not create anything")

	// 2. The real apply creates them.
	applied := decodeConvert(t, harness.post(t, source, false, body))
	r.False(applied.DryRun)
	r.Empty(applied.Errors, "no per-check errors expected: %+v", applied.Errors)
	r.Equal(preview.Converted, applied.Created)
	r.Equal(applied.Created, harness.countChecks(t))

	// 3. Re-importing the same payload is idempotent: updates, no new checks.
	again := decodeConvert(t, harness.post(t, source, false, body))
	r.Empty(again.Errors)
	r.Equal(0, again.Created, "re-import must not create duplicates")
	r.Equal(applied.Created, again.Updated)
	r.Equal(applied.Created, harness.countChecks(t))

	return applied
}

func TestConvertEndpointGatusRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarness(t, "")
	result := runSourceRoundTrip(t, harness, "gatus", readFixture(t, "gatus-config.yaml"))

	r.Equal("gatus", result.Manifest)
	r.NotEmpty(result.Warnings)

	check, err := harness.dbSvc.GetCheckByUidOrSlug(t.Context(), harness.org.UID, "back-end")
	r.NoError(err)
	r.Equal("http", check.Type)
}

func TestConvertEndpointUptimeKumaRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarness(t, "")
	result := runSourceRoundTrip(t, harness, "uptime-kuma", readFixture(t, "uptime-kuma-backup.json"))

	r.Equal("uptime-kuma", result.Manifest)

	check, err := harness.dbSvc.GetCheckByUidOrSlug(t.Context(), harness.org.UID, "website")
	r.NoError(err)
	r.Equal("http", check.Type)

	// Kuma groups became real check groups.
	groups, err := harness.dbSvc.ListCheckGroups(t.Context(), harness.org.UID)
	r.NoError(err)

	names := make([]string, 0, len(groups))
	for i := range groups {
		names = append(names, groups[i].Name)
	}

	r.Contains(names, "Production")
}

func TestConvertEndpointUptimeRobotRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarness(t, "")
	result := runSourceRoundTrip(t, harness, "uptimerobot", readFixture(t, "uptimerobot-monitors.json"))

	r.Equal("uptimerobot", result.Manifest)
	r.NotEmpty(result.Warnings)

	check, err := harness.dbSvc.GetCheckByUidOrSlug(t.Context(), harness.org.UID, "main-website")
	r.NoError(err)
	r.Equal("http", check.Type)
}

func TestConvertEndpointBetterStackRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newBetterStackServer(t)
	harness := newConvertHarness(t, srv.URL)

	body := betterStackBody(t, "")
	result := runSourceRoundTrip(t, harness, "betterstack", body)

	r.Equal("betterstack", result.Manifest)

	heartbeat, err := harness.dbSvc.GetCheckByUidOrSlug(t.Context(), harness.org.UID, "nightly-backup")
	r.NoError(err)
	r.Equal("heartbeat", heartbeat.Type)

	// The token is nowhere in the persisted check set.
	list, _, err := harness.dbSvc.ListChecks(t.Context(), harness.org.UID, &models.ListChecksFilter{})
	r.NoError(err)
	r.NotContains(fmt.Sprintf("%+v", list), testToken)
}

func TestConvertEndpointRejectsUnknownSource(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarness(t, "")
	rec := harness.post(t, "pingdom", false, []byte("endpoints: []"))
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
	r.Contains(rec.Body.String(), "source")
}

func TestConvertEndpointRejectsEmptyBody(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarness(t, "")
	rec := harness.post(t, "gatus", false, nil)
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
}

func TestConvertEndpointBetterStackBadTokenIsAValidationError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":"unauthorized ` + req.Header.Get("Authorization") + `"}`))
	}))
	t.Cleanup(srv.Close)

	harness := newConvertHarness(t, srv.URL)
	rec := harness.post(t, "betterstack", true, betterStackBody(t, ""))

	r.Equal(http.StatusBadRequest, rec.Code)

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal("VALIDATION_ERROR", body["code"])
	r.NotContains(rec.Body.String(), testToken, "the token must never be echoed back")
	r.Equal(0, harness.countChecks(t))
}

func TestConvertEndpointBetterStackUnreachableIsAValidationError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := srv.URL
	srv.Close()

	harness := newConvertHarness(t, baseURL)
	rec := harness.post(t, "betterstack", true, betterStackBody(t, ""))

	r.Equal(http.StatusBadRequest, rec.Code)

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal("VALIDATION_ERROR", body["code"])
	r.NotContains(rec.Body.String(), testToken)
}

func TestConvertEndpointBetterStackMissingTokenIsAValidationError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarness(t, "https://example.invalid")
	rec := harness.post(t, "betterstack", true, []byte(`{"token":""}`))

	r.Equal(http.StatusUnprocessableEntity, rec.Code)
	r.Contains(rec.Body.String(), "token")
}

func TestConvertEndpointUnknownOrganization(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarness(t, "")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/api/v1/orgs/nope/checks/import/convert?source=gatus",
		bytes.NewReader(readFixture(t, "gatus-config.yaml")))
	rec := httptest.NewRecorder()
	harness.router.ServeHTTP(rec, req)

	r.Equal(http.StatusNotFound, rec.Code)
}

// TestConvertEndpointNeverLogsTheBetterStackToken captures everything written
// to the default slog logger for the duration of a Better Stack conversion
// (both the happy path and the 401 path) and asserts the credential appears
// nowhere in it. The positive control guards the guard: if the capture were
// wired up wrong the test would pass vacuously.
//
//nolint:paralleltest // swaps the process-wide slog default logger
func TestConvertEndpointNeverLogsTheBetterStackToken(t *testing.T) {
	r := require.New(t)

	var buf bytes.Buffer

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	t.Cleanup(func() { slog.SetDefault(previous) })

	// Positive control: the capture really does see log output.
	slog.Info("importer log capture control", "sentinel", "log-capture-works")
	r.Contains(buf.String(), "log-capture-works")

	srv := newBetterStackServer(t)
	harness := newConvertHarness(t, srv.URL)
	r.Equal(http.StatusOK, harness.post(t, "betterstack", false, betterStackBody(t, "")).Code)

	// …and the failure path, which is the one that formats an error message.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":"unauthorized ` + req.Header.Get("Authorization") + `"}`))
	}))
	t.Cleanup(failing.Close)

	failHarness := newConvertHarness(t, failing.URL)
	r.Equal(http.StatusBadRequest, failHarness.post(t, "betterstack", true, betterStackBody(t, "")).Code)

	r.NotContains(buf.String(), testToken)
	r.NotContains(buf.String(), "Bearer ")
}
