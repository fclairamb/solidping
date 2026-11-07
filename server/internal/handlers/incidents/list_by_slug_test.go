package incidents_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkfilter"
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

// listBySlugFixture bundles an in-memory db.Service with an org, a check
// (addressed by slug), and an incident on that check — enough to exercise
// GET /incidents?checkUid=<slug> end to end through a real router, the way
// the check detail page's `useIncidents` call does.
type listBySlugFixture struct {
	dbSvc    *sqlite.Service
	org      *models.Organization
	check    *models.Check
	incident *models.Incident
}

func newListBySlugFixture(t *testing.T) *listBySlugFixture {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("list-by-slug-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "http-api-acme-io-datalake", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	inc := models.NewIncident(org.UID, check.UID, time.Now().Add(-5*time.Minute), "datalake is down")
	r.NoError(dbSvc.CreateIncident(ctx, inc))

	return &listBySlugFixture{dbSvc: dbSvc, org: org, check: check, incident: inc}
}

func newListBySlugRouter(f *listBySlugFixture) *httpx.Router {
	jobs := jobsvc.NewService(f.dbSvc.DB(), f.dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(f.dbSvc, jobs, clock.Real{}, nil)
	handler := incidents.NewHandler(svc, &config.Config{})

	router := httpx.New()
	router.GET("/orgs/:org/incidents", handler.ListIncidents)

	return router
}

type listIncidentsResponse struct {
	Data []struct {
		UID      string `json:"uid"`
		CheckUID string `json:"checkUid"`
	} `json:"data"`
}

// TestListIncidents_ByCheckSlug is the handler-level regression test for
// issue #127 / spec 2026-07-14-03: `GET /incidents?checkUid=<slug>` must
// resolve the slug the same way `/results` and every other check-scoped
// endpoint does, returning 200 with that check's incidents instead of
// forwarding the raw slug into a `check_uid uuid` column filter. On Postgres
// that raw-slug path fails with "invalid input syntax for type uuid" (see
// the companion embedded-Postgres test); here we pin the resolved-filtering
// contract itself, which holds regardless of backend.
func TestListIncidents_ByCheckSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newListBySlugFixture(t)
	router := newListBySlugRouter(f)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet,
		"/orgs/"+f.org.Slug+"/incidents?checkUid="+*f.check.Slug+"&limit=100", nil,
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, "slug-addressed checkUid must not 500: %s", rec.Body.String())

	var body listIncidentsResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Len(body.Data, 1, "the check's incident must be returned when filtering by its slug")
	r.Equal(f.incident.UID, body.Data[0].UID)
	r.Equal(f.check.UID, body.Data[0].CheckUID)
}

// TestListIncidents_ByCheckUID is the sibling case: filtering by the actual
// UID must keep working exactly as before — the fix must not regress the
// already-supported identifier form.
func TestListIncidents_ByCheckUID(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newListBySlugFixture(t)
	router := newListBySlugRouter(f)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet,
		"/orgs/"+f.org.Slug+"/incidents?checkUid="+f.check.UID+"&limit=100", nil,
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body listIncidentsResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Len(body.Data, 1)
	r.Equal(f.incident.UID, body.Data[0].UID)
}

// TestListIncidents_ByUnknownCheckIdentifier pins the "unknown identifier ->
// empty result, not an error" contract this fix adopts from `/results`: once
// checkUid is resolved rather than parsed as a raw uuid, a filter value that
// matches no check simply yields zero rows.
func TestListIncidents_ByUnknownCheckIdentifier(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newListBySlugFixture(t)
	router := newListBySlugRouter(f)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet,
		"/orgs/"+f.org.Slug+"/incidents?checkUid=does-not-exist&limit=100", nil,
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body listIncidentsResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Empty(body.Data)
}

// countingCheckLookups counts the per-identifier check lookups of the
// checkUid filter.
type countingCheckLookups struct {
	db.Service

	lookups atomic.Int32
}

//nolint:revive // overrides db.Service.GetCheckByUidOrSlug, the name is not ours
func (c *countingCheckLookups) GetCheckByUidOrSlug(
	ctx context.Context, orgUID, identifier string,
) (*models.Check, error) {
	c.lookups.Add(1)

	return c.Service.GetCheckByUidOrSlug(ctx, orgUID, identifier)
}

func repeatIdentifier(id string, n int) []string {
	ids := make([]string, n)
	for idx := range ids {
		ids[idx] = id
	}

	return ids
}

func listIncidentsByCheckUID(t *testing.T, f *listBySlugFixture, checkUID string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet,
		"/orgs/"+f.org.Slug+"/incidents?checkUid="+checkUID, nil,
	)
	rec := httptest.NewRecorder()
	newListBySlugRouter(f).ServeHTTP(rec, req)

	return rec
}

// TestListIncidents_RejectsOversizedCheckFilter: more than the cap is a 400
// VALIDATION_ERROR naming checkUid, even when every entry repeats one slug
// (security scan 2026-11-07, AG1). The cap itself is still accepted.
func TestListIncidents_RejectsOversizedCheckFilter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newListBySlugFixture(t)

	rec := listIncidentsByCheckUID(t, f,
		strings.Join(repeatIdentifier(*f.check.Slug, checkfilter.MaxIdentifiers+1), ","))
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())

	var body struct {
		Code string `json:"code"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal("VALIDATION_ERROR", body.Code)
	r.Contains(rec.Body.String(), "checkUid")

	rec = listIncidentsByCheckUID(t, f,
		strings.Join(repeatIdentifier(*f.check.Slug, checkfilter.MaxIdentifiers), ","))
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
}

// TestListIncidents_LooksUpEachDistinctIdentifierOnce: a list that repeats
// one slug up to the cap costs one lookup, not one per entry.
func TestListIncidents_LooksUpEachDistinctIdentifierOnce(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newListBySlugFixture(t)
	counting := &countingCheckLookups{Service: f.dbSvc}
	jobs := jobsvc.NewService(f.dbSvc.DB(), f.dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(counting, jobs, clock.Real{}, nil)

	resp, err := svc.ListIncidents(t.Context(), f.org.Slug, &incidents.ListIncidentsOptions{
		CheckUIDs: repeatIdentifier(*f.check.Slug, checkfilter.MaxIdentifiers),
		Size:      20,
	})
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Equal(int32(1), counting.lookups.Load())

	// A canceled request stops before any lookup.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = svc.ListIncidents(ctx, f.org.Slug, &incidents.ListIncidentsOptions{
		CheckUIDs: []string{"a", "b"},
		Size:      20,
	})
	r.Error(err)
	r.Equal(int32(1), counting.lookups.Load())
}

// TestListIncidents_CheckFilterByUIDAndSlug is the positive control: two real
// checks, one by UID and one by slug, plus an unknown identifier, return
// exactly those two checks' incidents; only unknown identifiers still give
// the explicit empty page.
func TestListIncidents_CheckFilterByUIDAndSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	f := newListBySlugFixture(t)

	second := models.NewCheck(f.org.UID, "db-acme-io", "http")
	r.NoError(f.dbSvc.CreateCheck(ctx, second))
	secondInc := models.NewIncident(f.org.UID, second.UID, time.Now().Add(-4*time.Minute), "db is down")
	r.NoError(f.dbSvc.CreateIncident(ctx, secondInc))

	other := models.NewCheck(f.org.UID, "cdn-acme-io", "http")
	r.NoError(f.dbSvc.CreateCheck(ctx, other))
	r.NoError(f.dbSvc.CreateIncident(ctx,
		models.NewIncident(f.org.UID, other.UID, time.Now().Add(-3*time.Minute), "cdn is down")))

	rec := listIncidentsByCheckUID(t, f, f.check.UID+","+*second.Slug+",does-not-exist")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body listIncidentsResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))

	got := make([]string, 0, len(body.Data))
	for _, row := range body.Data {
		got = append(got, row.UID)
	}
	r.ElementsMatch([]string{f.incident.UID, secondInc.UID}, got)

	rec = listIncidentsByCheckUID(t, f, "does-not-exist,nor-this,does-not-exist")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), `"data":[]`)
}
