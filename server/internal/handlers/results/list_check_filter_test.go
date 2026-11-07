package results

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkfilter"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// countingDB counts the per-identifier check lookups of the checkUid filter.
type countingDB struct {
	db.Service

	lookups atomic.Int32
}

//nolint:revive // overrides db.Service.GetCheckByUidOrSlug, the name is not ours
func (c *countingDB) GetCheckByUidOrSlug(
	ctx context.Context, orgUID, identifier string,
) (*models.Check, error) {
	c.lookups.Add(1)

	return c.Service.GetCheckByUidOrSlug(ctx, orgUID, identifier)
}

func repeatedIdentifiers(id string, n int) []string {
	ids := make([]string, n)
	for idx := range ids {
		ids[idx] = id
	}

	return ids
}

// seedCheckFilterResults creates an org with three checks, each with one raw
// result, so a filter that degraded to "no filter" would be caught.
func seedCheckFilterResults(t *testing.T) (db.Service, *models.Organization, []*models.Check) {
	t.Helper()

	r := require.New(t)
	dbSvc, ctx := newNeighborsTestDB(t)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	checks := make([]*models.Check, 0, 3)
	for idx, slug := range []string{"api-prod", "db-prod", "cdn-prod"} {
		check := newBareCheck(ctx, dbSvc, r, org.UID, slug)
		r.NoError(dbSvc.CreateResult(ctx, &models.Result{
			UID:             uuid.Must(uuid.NewV7()).String(),
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			PeriodType:      "raw",
			PeriodStart:     time.Now().Add(-time.Duration(idx+1) * time.Minute),
		}))
		checks = append(checks, check)
	}

	return dbSvc, org, checks
}

func listResultsHTTP(t *testing.T, dbSvc db.Service, orgSlug, checkUID string) *httptest.ResponseRecorder {
	t.Helper()

	router := httpx.New()
	router.GET("/orgs/:org/results", NewHandler(NewService(dbSvc, nil), &config.Config{}).ListResults)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/orgs/"+orgSlug+"/results?checkUid="+checkUID, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// TestListResultsRejectsOversizedCheckFilter: more than the cap is a 400
// VALIDATION_ERROR naming checkUid, even when every entry repeats one slug
// (security scan 2026-11-07, AG1). The cap itself is still accepted.
func TestListResultsRejectsOversizedCheckFilter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dbSvc, org, checks := seedCheckFilterResults(t)
	slug := *checks[0].Slug

	rec := listResultsHTTP(t, dbSvc, org.Slug,
		strings.Join(repeatedIdentifiers(slug, checkfilter.MaxIdentifiers+1), ","))
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())

	var body struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal("VALIDATION_ERROR", body.Code)
	r.Contains(rec.Body.String(), "checkUid")

	rec = listResultsHTTP(t, dbSvc, org.Slug,
		strings.Join(repeatedIdentifiers(slug, checkfilter.MaxIdentifiers), ","))
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
}

// TestResolveCheckIdentifiersLooksUpEachDistinctIdentifierOnce: a list that
// repeats one slug up to the cap costs one lookup, not one per entry.
func TestResolveCheckIdentifiersLooksUpEachDistinctIdentifierOnce(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dbSvc, org, checks := seedCheckFilterResults(t)
	counting := &countingDB{Service: dbSvc}
	svc := NewService(counting, nil)

	ids := repeatedIdentifiers(" "+*checks[0].Slug+" ", checkfilter.MaxIdentifiers)
	uids, err := svc.resolveCheckIdentifiers(t.Context(), org.UID, ids)
	r.NoError(err)
	r.Equal([]string{checks[0].UID}, uids)
	r.Equal(int32(1), counting.lookups.Load())
}

// TestResolveCheckIdentifiersStopsOnCanceledContext: a canceled request stops
// the loop instead of running every remaining lookup.
func TestResolveCheckIdentifiersStopsOnCanceledContext(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dbSvc, org, _ := seedCheckFilterResults(t)
	counting := &countingDB{Service: dbSvc}
	svc := NewService(counting, nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := svc.resolveCheckIdentifiers(ctx, org.UID, []string{"a", "b", "c"})
	r.ErrorIs(err, context.Canceled)
	r.Equal(int32(0), counting.lookups.Load())
}

// TestListResultsCheckFilterByUIDAndSlug is the positive control: two real
// checks, one by UID and one by slug, plus an unknown identifier, return
// exactly those two checks' results.
func TestListResultsCheckFilterByUIDAndSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dbSvc, org, checks := seedCheckFilterResults(t)

	rec := listResultsHTTP(t, dbSvc, org.Slug, checks[0].UID+","+*checks[1].Slug+",does-not-exist")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body ListResultsResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))

	got := make([]string, 0, len(body.Data))
	for _, row := range body.Data {
		got = append(got, row.CheckUID)
	}
	r.ElementsMatch([]string{checks[0].UID, checks[1].UID}, got)
}
