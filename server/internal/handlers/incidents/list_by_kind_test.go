package incidents_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
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

// listByKindFixture seeds one incident of each kind in one org, so a `kind`
// filter can be shown to NARROW the list (and the unfiltered call, returning
// all three, is the positive control).
type listByKindFixture struct {
	org    *models.Organization
	byKind map[string]string // kind -> incident uid
}

func seedIncidentsOfEveryKind(t *testing.T, dbSvc db.Service, orgSlug string) *listByKindFixture {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	org := models.NewOrganization(orgSlug, "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "region-heartbeat-acme", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	fixture := &listByKindFixture{org: org, byKind: map[string]string{}}

	kinds := []string{models.IncidentKindCheck, models.IncidentKindDegraded, models.IncidentKindSLOBurn}
	for i, kind := range kinds {
		inc := models.NewIncident(org.UID, check.UID, time.Now().Add(-time.Duration(i+1)*time.Minute), kind+" incident")
		inc.Kind = kind
		r.NoError(dbSvc.CreateIncident(ctx, inc))

		fixture.byKind[kind] = inc.UID
	}

	return fixture
}

func newListByKindRouter(dbSvc *sqlite.Service) *httpx.Router {
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)
	handler := incidents.NewHandler(svc, &config.Config{})

	router := httpx.New()
	router.GET("/orgs/:org/incidents", handler.ListIncidents)

	return router
}

func sortedUIDs(uids ...string) []string {
	out := append([]string(nil), uids...)
	sort.Strings(out)

	return out
}

// TestListIncidents_KindFilter pins the `kind` query parameter of
// GET /incidents (spec 2026-09-27-02): absent or empty means every kind,
// one or several comma-separated kinds narrow the list, and any value outside
// check / degraded / slo_burn is a 400 VALIDATION_ERROR rather than a filter
// that silently matches nothing.
func TestListIncidents_KindFilter(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	f := seedIncidentsOfEveryKind(t, dbSvc, "list-by-kind-org")
	router := newListByKindRouter(dbSvc)

	all := sortedUIDs(
		f.byKind[models.IncidentKindCheck],
		f.byKind[models.IncidentKindDegraded],
		f.byKind[models.IncidentKindSLOBurn],
	)

	tests := []struct {
		name     string
		query    string
		wantCode int
		wantUIDs []string
	}{
		{name: "absent returns every kind", query: "", wantCode: http.StatusOK, wantUIDs: all},
		{name: "empty returns every kind", query: "kind=", wantCode: http.StatusOK, wantUIDs: all},
		{
			name: "single kind", query: "kind=degraded", wantCode: http.StatusOK,
			wantUIDs: []string{f.byKind[models.IncidentKindDegraded]},
		},
		{
			name: "two kinds", query: "kind=check,slo_burn", wantCode: http.StatusOK,
			wantUIDs: sortedUIDs(f.byKind[models.IncidentKindCheck], f.byKind[models.IncidentKindSLOBurn]),
		},
		{name: "unknown kind", query: "kind=outage", wantCode: http.StatusBadRequest},
		{name: "one unknown among valid kinds", query: "kind=check,bogus", wantCode: http.StatusBadRequest},
		{name: "wrong case", query: "kind=Degraded", wantCode: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			req := httptest.NewRequestWithContext(
				t.Context(), http.MethodGet, "/orgs/"+f.org.Slug+"/incidents?limit=100&"+tc.query, nil,
			)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			r.Equal(tc.wantCode, rec.Code, rec.Body.String())

			if tc.wantCode != http.StatusOK {
				var errBody struct {
					Code string `json:"code"`
				}
				r.NoError(json.Unmarshal(rec.Body.Bytes(), &errBody))
				r.Equal("VALIDATION_ERROR", errBody.Code)

				return
			}

			var body struct {
				Data []struct {
					UID  string `json:"uid"`
					Kind string `json:"kind"`
				} `json:"data"`
			}
			r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))

			got := make([]string, 0, len(body.Data))
			for _, inc := range body.Data {
				got = append(got, inc.UID)
				r.Equal(f.byKind[inc.Kind], inc.UID, "the response must report each incident's own kind")
			}

			r.Equal(tc.wantUIDs, sortedUIDs(got...))
		})
	}
}
