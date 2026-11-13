package incidents_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

func TestGetInsights(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("insights-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	other := models.NewOrganization("insights-other", "")
	r.NoError(dbSvc.CreateOrganization(ctx, other))

	check := models.NewCheck(org.UID, "api-acme", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))
	otherCheck := models.NewCheck(other.UID, "api-other", "http")
	r.NoError(dbSvc.CreateCheck(ctx, otherCheck))

	started := time.Now().Add(-2 * time.Hour)
	mk := func(orgUID, checkUID, kind string, suppressed, acked bool) {
		inc := models.NewIncident(orgUID, checkUID, started, "x")
		inc.Kind = kind
		inc.PagingSuppressed = suppressed

		if acked {
			at := started.Add(time.Minute)
			inc.AcknowledgedAt = &at
		}

		r.NoError(dbSvc.CreateIncident(ctx, inc))
	}

	mk(org.UID, check.UID, models.IncidentKindCheck, false, true)         // counted, acked
	mk(org.UID, check.UID, models.IncidentKindCheck, true, true)          // rolled up: counted, not in ack rate
	mk(org.UID, check.UID, models.IncidentKindSLOBurn, false, false)      // burn: ignored
	mk(other.UID, otherCheck.UID, models.IncidentKindCheck, false, false) // other org: ignored

	router := httpx.New()
	router.GET("/orgs/:org/incidents/insights", incidents.NewHandler(
		incidents.NewService(dbSvc, nil, clock.Real{}, nil), &config.Config{}).GetInsights)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))

		return rec
	}

	rec := get("/orgs/insights-org/incidents/insights?period=1y")
	r.Equal(http.StatusBadRequest, rec.Code)
	r.Contains(rec.Body.String(), "VALIDATION_ERROR")

	rec = get("/orgs/insights-org/incidents/insights")
	r.Equal(http.StatusOK, rec.Code)

	var body incidents.InsightsResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal("30d", body.Period)
	r.Equal(2, body.Summary.IncidentCount, "burn and other-org incidents are not counted")
	r.Equal(1, body.Summary.PagedCount, "rolled-up incident is out of the ack rate")
	r.InDelta(1.0, body.Summary.AckRate, 0.0001)
	r.Len(body.Checks, 1)
	r.Equal(2, body.Checks[0].IncidentCount)
	r.Equal(check.UID, body.Checks[0].CheckUID)
	r.Equal("api-acme", *body.Checks[0].Slug)
	r.False(body.Truncated)
}

func newInsightsRouter(t *testing.T, dbSvc db.Service, limits ...int) *httpx.Router {
	t.Helper()

	svc := incidents.NewService(dbSvc, nil, clock.Real{}, nil)
	if len(limits) == 2 {
		svc.SetInsightsLimitsForTest(limits[0], limits[1])
	}

	router := httpx.New()
	router.GET("/orgs/:org/incidents/insights", incidents.NewHandler(svc, &config.Config{}).GetInsights)

	return router
}

func getInsights(t *testing.T, router *httpx.Router, org string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/orgs/"+org+"/incidents/insights?period=7d", nil))

	return rec
}

func newInsightsDB(t *testing.T) db.Service {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	require.NoError(t, dbSvc.Initialize(t.Context()))

	return dbSvc
}

// TestGetInsightsWindowOverlap: incidents active during the period count even
// when they started before it; one resolved before the window does not.
func TestGetInsightsWindowOverlap(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	dbSvc := newInsightsDB(t)

	org := models.NewOrganization("overlap-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	openCheck := models.NewCheck(org.UID, "still-open", "http")
	r.NoError(dbSvc.CreateCheck(ctx, openCheck))
	oldCheck := models.NewCheck(org.UID, "long-resolved", "http")
	r.NoError(dbSvc.CreateCheck(ctx, oldCheck))
	crossCheck := models.NewCheck(org.UID, "resolved-inside", "http")
	r.NoError(dbSvc.CreateCheck(ctx, crossCheck))

	now := time.Now()
	started := now.Add(-20 * 24 * time.Hour) // before the 7d window

	open := models.NewIncident(org.UID, openCheck.UID, started, "x")
	r.NoError(dbSvc.CreateIncident(ctx, open))

	oldResolvedAt := now.Add(-10 * 24 * time.Hour)
	old := models.NewIncident(org.UID, oldCheck.UID, started, "x")
	old.ResolvedAt = &oldResolvedAt
	r.NoError(dbSvc.CreateIncident(ctx, old))

	crossResolvedAt := now.Add(-3 * 24 * time.Hour)
	cross := models.NewIncident(org.UID, crossCheck.UID, started, "x")
	cross.ResolvedAt = &crossResolvedAt
	r.NoError(dbSvc.CreateIncident(ctx, cross))

	rec := getInsights(t, newInsightsRouter(t, dbSvc), org.Slug)
	r.Equal(http.StatusOK, rec.Code)

	var body incidents.InsightsResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(2, body.Summary.IncidentCount, "open and resolved-inside count, resolved-before does not")

	slugs := map[string]bool{}
	for _, row := range body.Checks {
		slugs[*row.Slug] = true
	}

	r.True(slugs["still-open"], "positive control: open outage from before the window")
	r.True(slugs["resolved-inside"])
	r.False(slugs["long-resolved"], "negative case: resolved before the window start")
}

// TestGetInsightsPaginationAndTruncation uses tiny page size and cap.
func TestGetInsightsPaginationAndTruncation(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	dbSvc := newInsightsDB(t)

	org := models.NewOrganization("paging-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "paged", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	for idx := range 7 {
		inc := models.NewIncident(org.UID, check.UID, time.Now().Add(-time.Duration(idx+1)*time.Hour), "x")
		r.NoError(dbSvc.CreateIncident(ctx, inc))
	}

	decode := func(rec *httptest.ResponseRecorder) incidents.InsightsResponse {
		r.Equal(http.StatusOK, rec.Code)

		var body incidents.InsightsResponse
		r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))

		return body
	}

	// 7 incidents over pages of 3: every page is followed, nothing truncated.
	body := decode(getInsights(t, newInsightsRouter(t, dbSvc, 3, 100), org.Slug))
	r.Equal(7, body.Summary.IncidentCount)
	r.False(body.Truncated)

	// Cap of 5 over pages of 3: truncated at exactly 5.
	body = decode(getInsights(t, newInsightsRouter(t, dbSvc, 3, 5), org.Slug))
	r.Equal(5, body.Summary.IncidentCount)
	r.True(body.Truncated)
}

var errChecksLookup = errors.New("checks lookup failed")

type failingChecksDB struct {
	db.Service
}

func (failingChecksDB) GetChecksByUIDs(context.Context, string, []string) (map[string]*models.Check, error) {
	return nil, errChecksLookup
}

// TestGetInsightsChecksLookupError: a failed check lookup is a 500, not a
// silently nameless table.
func TestGetInsightsChecksLookupError(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	dbSvc := newInsightsDB(t)

	org := models.NewOrganization("lookup-err-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "chk-lookup", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))
	r.NoError(dbSvc.CreateIncident(ctx, models.NewIncident(org.UID, check.UID, time.Now().Add(-time.Hour), "x")))

	rec := getInsights(t, newInsightsRouter(t, failingChecksDB{dbSvc}), org.Slug)
	r.Equal(http.StatusInternalServerError, rec.Code)
	r.Contains(rec.Body.String(), "INTERNAL_ERROR")
}
