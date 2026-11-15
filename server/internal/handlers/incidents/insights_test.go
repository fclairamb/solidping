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
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

func newInsightsRouter(dbSvc *sqlite.Service) *httpx.Router {
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)
	handler := incidents.NewHandler(svc, &config.Config{})

	router := httpx.New()
	router.GET("/orgs/:org/incidents/insights", handler.GetInsights)

	return router
}

func fetchInsights(
	t *testing.T, router *httpx.Router, org, query string,
) (*httptest.ResponseRecorder, incidents.InsightsResponse) {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/orgs/"+org+"/incidents/insights"+query, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var out incidents.InsightsResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	}

	return rec, out
}

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
	empty := models.NewOrganization("insights-empty", "")
	r.NoError(dbSvc.CreateOrganization(ctx, empty))

	check := models.NewCheck(org.UID, "acme-api", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))
	otherCheck := models.NewCheck(other.UID, "acme-other", "http")
	r.NoError(dbSvc.CreateCheck(ctx, otherCheck))

	now := time.Now()
	seed := func(orgUID, checkUID, kind string, mod func(*models.Incident)) {
		inc := models.NewIncident(orgUID, checkUID, now.Add(-2*time.Hour), "incident")
		inc.Kind = kind
		if mod != nil {
			mod(inc)
		}
		r.NoError(dbSvc.CreateIncident(ctx, inc))
	}

	ackedAt := now.Add(-2*time.Hour + 5*time.Minute)
	seed(org.UID, check.UID, models.IncidentKindCheck, func(i *models.Incident) { i.AcknowledgedAt = &ackedAt })
	// Rolled up: counted in the checks table, left out of the ack rate.
	seed(org.UID, check.UID, models.IncidentKindCheck, func(i *models.Incident) { i.PagingSuppressed = true })
	// Not counted: another kind, and another org.
	seed(org.UID, check.UID, models.IncidentKindSLOBurn, nil)
	seed(other.UID, otherCheck.UID, models.IncidentKindCheck, nil)

	router := newInsightsRouter(dbSvc)

	t.Run("counts check outages of this org only", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		rec, got := fetchInsights(t, router, org.Slug, "")
		r.Equal(http.StatusOK, rec.Code, rec.Body.String())
		r.Equal("30d", got.Period)
		r.Equal(2, got.Summary.IncidentCount)
		r.Len(got.Checks, 1)
		r.Equal(2, got.Checks[0].IncidentCount)
		r.Equal("acme-api", got.Checks[0].CheckSlug)
		r.False(got.Truncated)
	})

	t.Run("rolled-up incident is not in the ack rate", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		_, got := fetchInsights(t, router, org.Slug, "?period=7d")
		r.Equal(1, got.Summary.PagedCount)
		r.Equal(1, got.Summary.AcknowledgedCount)
		r.InDelta(1.0, got.Summary.AckRate, 0)
		r.Equal(int64(300), *got.Summary.TimeToAcknowledge.MedianSeconds)
	})

	t.Run("period validation", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		rec, _ := fetchInsights(t, router, org.Slug, "?period=1y")
		r.Equal(http.StatusBadRequest, rec.Code)

		var body struct {
			Code string `json:"code"`
		}
		r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
		r.Equal("VALIDATION_ERROR", body.Code)
	})

	t.Run("empty org", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		rec, got := fetchInsights(t, router, empty.Slug, "?period=90d")
		r.Equal(http.StatusOK, rec.Code)
		r.Zero(got.Summary.IncidentCount)
		r.Empty(got.Checks)
		r.Nil(got.Summary.TimeToAcknowledge.MedianSeconds)
	})
}

func newInsightsDB(t *testing.T) *sqlite.Service {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	require.NoError(t, dbSvc.Initialize(t.Context()))

	return dbSvc
}

func newInsightsRouterFor(dbSvc db.Service, limits ...int) *httpx.Router {
	svc := incidents.NewService(dbSvc, nil, clock.Real{}, nil)
	if len(limits) == 2 {
		svc.SetInsightsLimitsForTest(limits[0], limits[1])
	}

	router := httpx.New()
	router.GET("/orgs/:org/incidents/insights", incidents.NewHandler(svc, &config.Config{}).GetInsights)

	return router
}

// TestGetInsightsWindowOverlap: incidents active during the period count even
// when they started before it; one resolved before the window start does not.
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

	r.NoError(dbSvc.CreateIncident(ctx, models.NewIncident(org.UID, openCheck.UID, started, "x")))

	oldResolvedAt := now.Add(-10 * 24 * time.Hour)
	old := models.NewIncident(org.UID, oldCheck.UID, started, "x")
	old.ResolvedAt = &oldResolvedAt
	r.NoError(dbSvc.CreateIncident(ctx, old))

	crossResolvedAt := now.Add(-3 * 24 * time.Hour)
	cross := models.NewIncident(org.UID, crossCheck.UID, started, "x")
	cross.ResolvedAt = &crossResolvedAt
	r.NoError(dbSvc.CreateIncident(ctx, cross))

	rec, got := fetchInsights(t, newInsightsRouterFor(dbSvc), org.Slug, "?period=7d")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Equal(2, got.Summary.IncidentCount, "open and resolved-inside count, resolved-before does not")

	slugs := map[string]bool{}
	for _, row := range got.Checks {
		slugs[row.CheckSlug] = true
	}

	r.True(slugs["still-open"], "positive control: an open outage from before the window")
	r.True(slugs["resolved-inside"])
	r.False(slugs["long-resolved"], "resolved before the window start")
}

// TestGetInsightsPaginationAndTruncation uses a tiny page size and cap.
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

	// 7 incidents over pages of 3: every page is followed, nothing truncated.
	rec, got := fetchInsights(t, newInsightsRouterFor(dbSvc, 3, 100), org.Slug, "?period=7d")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Equal(7, got.Summary.IncidentCount)
	r.False(got.Truncated)

	// A cap of 5 over pages of 3: truncated at exactly 5.
	rec, got = fetchInsights(t, newInsightsRouterFor(dbSvc, 3, 5), org.Slug, "?period=7d")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Equal(5, got.Summary.IncidentCount)
	r.True(got.Truncated)
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

	rec, _ := fetchInsights(t, newInsightsRouterFor(failingChecksDB{dbSvc}), org.Slug, "?period=7d")
	r.Equal(http.StatusInternalServerError, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), "INTERNAL_ERROR")
}
