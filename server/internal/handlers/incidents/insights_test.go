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
