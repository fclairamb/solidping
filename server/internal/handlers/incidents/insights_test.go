package incidents_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
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
