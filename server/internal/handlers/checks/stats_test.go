package checks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// statsFixture describes one check row to seed for a stats aggregation case.
type statsFixture struct {
	status   models.CheckStatus
	enabled  bool
	internal bool
	deleted  bool
}

// seedStatsChecks writes the fixture rows straight through the db service —
// no CreateCheck round-trip — so a case can pin an exact (status, enabled,
// internal, deleted) combination that the public API would otherwise not let
// it reach (e.g. a freshly created check is always `created`).
func seedStatsChecks(
	ctx context.Context, t *testing.T, dbSvc db.Service, orgUID string, fixtures []statsFixture,
) {
	t.Helper()
	r := require.New(t)

	for _, f := range fixtures {
		// Unique slug per row: seedStatsChecks is called more than once
		// against the same org (the cache tests seed, then seed again).
		check := models.NewCheck(orgUID, "c"+uuid.NewString(), "http")
		check.Config = models.JSONMap{"url": "https://example.com"}
		check.Status = f.status
		check.Enabled = f.enabled
		check.Internal = f.internal
		r.NoError(dbSvc.CreateCheck(ctx, check))

		if f.deleted {
			r.NoError(dbSvc.DeleteCheck(ctx, check.UID))
		}
	}
}

// newStatsService builds a checks service over a fresh in-memory SQLite DB
// with one organization.
func newStatsService(t *testing.T, slug string) (*checks.Service, db.Service, *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization(slug, "Stats Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	return svc, dbSvc, org
}

// statsAggregationCases is shared by the SQLite suite below and by the
// embedded-Postgres twin in stats_postgres_test.go, so both dialects are held
// to byte-identical expectations.
func statsAggregationCases() []struct {
	name     string
	fixtures []statsFixture
	expected checks.CheckStatsResponse
} {
	return []struct {
		name     string
		fixtures []statsFixture
		expected checks.CheckStatsResponse
	}{
		{
			name:     "empty org",
			fixtures: nil,
			expected: checks.CheckStatsResponse{
				Total: 0, Enabled: 0, Disabled: 0, Down: 0, HardDown: 0,
				ByStatus: map[string]int{},
			},
		},
		{
			name: "mixed statuses, all enabled",
			fixtures: []statsFixture{
				{status: models.CheckStatusUp, enabled: true},
				{status: models.CheckStatusUp, enabled: true},
				{status: models.CheckStatusDown, enabled: true},
				{status: models.CheckStatusWarning, enabled: true},
				{status: models.CheckStatusCreated, enabled: true},
				{status: models.CheckStatusValidating, enabled: true},
				{status: models.CheckStatusDegraded, enabled: true},
			},
			expected: checks.CheckStatsResponse{
				Total: 7, Enabled: 7, Disabled: 0, Down: 1, HardDown: 1,
				ByStatus: map[string]int{
					models.WireStatusUp:         2,
					models.WireStatusDown:       1,
					models.WireStatusWarning:    1,
					models.WireStatusCreated:    1,
					models.WireStatusValidating: 1,
					models.WireStatusDegraded:   1,
				},
			},
		},
		{
			name: "enabled and disabled split, disabled checks still counted by status",
			fixtures: []statsFixture{
				{status: models.CheckStatusUp, enabled: true},
				{status: models.CheckStatusUp, enabled: false},
				{status: models.CheckStatusDown, enabled: false},
				{status: models.CheckStatusDown, enabled: true},
			},
			expected: checks.CheckStatsResponse{
				// down/hardDown span disabled checks too — the dashboard's
				// downCount filters on status alone, never on `enabled`.
				Total: 4, Enabled: 2, Disabled: 2, Down: 2, HardDown: 2,
				ByStatus: map[string]int{
					models.WireStatusUp:   2,
					models.WireStatusDown: 2,
				},
			},
		},
		{
			name: "internal checks are excluded, matching the list endpoint default",
			fixtures: []statsFixture{
				{status: models.CheckStatusUp, enabled: true},
				{status: models.CheckStatusDown, enabled: true, internal: true},
				{status: models.CheckStatusUp, enabled: true, internal: true},
			},
			expected: checks.CheckStatsResponse{
				Total: 1, Enabled: 1, Disabled: 0, Down: 0, HardDown: 0,
				ByStatus: map[string]int{models.WireStatusUp: 1},
			},
		},
		{
			name: "soft-deleted checks are excluded",
			fixtures: []statsFixture{
				{status: models.CheckStatusUp, enabled: true},
				{status: models.CheckStatusDown, enabled: true, deleted: true},
			},
			expected: checks.CheckStatsResponse{
				Total: 1, Enabled: 1, Disabled: 0, Down: 0, HardDown: 0,
				ByStatus: map[string]int{models.WireStatusUp: 1},
			},
		},
	}
}

// requireStatsEqual compares an actual response to an expectation whose
// ByStatus only lists the non-zero buckets: every known status key must be
// present in the response (clients index it unguarded), and any key the
// expectation omits must be zero.
func requireStatsEqual(t *testing.T, expected, actual checks.CheckStatsResponse) {
	t.Helper()
	r := require.New(t)

	r.Equal(expected.Total, actual.Total, "total")
	r.Equal(expected.Enabled, actual.Enabled, "enabled")
	r.Equal(expected.Disabled, actual.Disabled, "disabled")
	r.Equal(expected.Down, actual.Down, "down")
	r.Equal(expected.HardDown, actual.HardDown, "hardDown")

	knownStatuses := []string{
		models.WireStatusCreated,
		models.WireStatusUp,
		models.WireStatusDown,
		models.WireStatusValidating,
		models.WireStatusDegraded,
		models.WireStatusWarning,
		models.WireStatusUnknown,
	}

	for _, key := range knownStatuses {
		got, ok := actual.ByStatus[key]
		r.True(ok, "byStatus must always carry the %q key so clients can index it unguarded", key)
		r.Equal(expected.ByStatus[key], got, "byStatus[%q]", key)
	}

	r.Len(actual.ByStatus, len(knownStatuses), "byStatus must carry exactly the known statuses")
}

// TestGetCheckStatsAggregation_SQLite is the table-driven aggregation suite on
// SQLite. Its embedded-Postgres twin lives in stats_postgres_test.go and runs
// the same cases through statsAggregationCases().
func TestGetCheckStatsAggregation_SQLite(t *testing.T) {
	t.Parallel()

	for i, tc := range statsAggregationCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx := t.Context()

			svc, dbSvc, org := newStatsService(t, fmt.Sprintf("stats-agg-%d", i))
			seedStatsChecks(ctx, t, dbSvc, org.UID, tc.fixtures)

			stats, err := svc.GetCheckStats(ctx, org.Slug)
			r.NoError(err)
			requireStatsEqual(t, tc.expected, stats)
		})
	}
}

// TestGetCheckStatsBeyondPageClamp_SQLite is the regression proof for GitHub
// issue #172: with 250 checks the stats endpoint must report all 250, a number
// the old client-side counting could never produce because the list endpoint
// clamps `limit` to 100 rows per page and the dashboard never paginated.
//
// The assertions are deliberately framed against that clamp: totals must
// exceed 100, and the down count must exceed what a single 100-row page could
// contain even in the worst case.
func TestGetCheckStatsBeyondPageClamp_SQLite(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := newStatsService(t, "stats-clamp")

	const (
		upCount       = 130
		downCount     = 105
		disabledCount = 15
	)

	fixtures := make([]statsFixture, 0, upCount+downCount+disabledCount)
	for range upCount {
		fixtures = append(fixtures, statsFixture{status: models.CheckStatusUp, enabled: true})
	}

	for range downCount {
		fixtures = append(fixtures, statsFixture{status: models.CheckStatusDown, enabled: true})
	}

	for range disabledCount {
		fixtures = append(fixtures, statsFixture{status: models.CheckStatusUp, enabled: false})
	}

	seedStatsChecks(ctx, t, dbSvc, org.UID, fixtures)

	handler := checks.NewHandler(svc, &config.Config{})
	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	group.GET("", handler.ListChecks)
	group.GET("/stats", handler.GetCheckStats)

	// Positive control for the bug: the list endpoint really does clamp a page
	// to 100 rows even when the dashboard asks for limit=1000, so any counter
	// derived from one page physically cannot see this fleet. Asserted, not
	// assumed — if this ever stops clamping, the >100 proof below would pass
	// for the wrong reason.
	listReq := httptest.NewRequestWithContext(
		ctx, http.MethodGet, "/api/v1/orgs/"+org.Slug+"/checks?limit=1000", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	r.Equal(http.StatusOK, listRec.Code, listRec.Body.String())

	var listPayload struct {
		Data []map[string]any `json:"data"`
	}

	r.NoError(json.Unmarshal(listRec.Body.Bytes(), &listPayload))
	r.Len(listPayload.Data, 100, "list endpoint must still clamp a page to 100 rows")

	// The stats endpoint, over the same data, sees the whole fleet.
	statsReq := httptest.NewRequestWithContext(
		ctx, http.MethodGet, "/api/v1/orgs/"+org.Slug+"/checks/stats", nil)
	statsRec := httptest.NewRecorder()
	router.ServeHTTP(statsRec, statsReq)
	r.Equal(http.StatusOK, statsRec.Code, statsRec.Body.String())

	var stats checks.CheckStatsResponse

	r.NoError(json.Unmarshal(statsRec.Body.Bytes(), &stats))
	r.Greater(stats.Total, len(listPayload.Data),
		"stats must report more checks than one clamped page can carry")

	r.Equal(upCount+downCount+disabledCount, stats.Total)
	r.Greater(stats.Total, 100, "total must exceed the 100-row page clamp")
	r.Equal(upCount+downCount, stats.Enabled)
	r.Equal(disabledCount, stats.Disabled)
	r.Equal(downCount, stats.Down)
	r.Greater(stats.Down, 100, "down count must exceed what one clamped page could ever report")
	r.Equal(downCount, stats.HardDown)
	r.Equal(upCount+disabledCount, stats.ByStatus[models.WireStatusUp])
	r.Equal(downCount, stats.ByStatus[models.WireStatusDown])
}

// TestGetCheckStatsEndpointRouting exercises the HTTP surface: /checks/stats
// must resolve to the stats handler and not be swallowed by the
// /checks/:checkUid route (the classic ordering bug), and it must return the
// documented JSON shape with camelCase keys.
func TestGetCheckStatsEndpointRouting(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := newStatsService(t, "stats-http")
	seedStatsChecks(ctx, t, dbSvc, org.UID, []statsFixture{
		{status: models.CheckStatusUp, enabled: true},
		{status: models.CheckStatusDown, enabled: true},
		{status: models.CheckStatusUp, enabled: false},
	})

	handler := checks.NewHandler(svc, &config.Config{})
	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	// Same registration order as internal/app/server.go: stats first.
	group.GET("/stats", handler.GetCheckStats)
	group.GET("/:checkUid", handler.GetCheck)

	req := httptest.NewRequestWithContext(
		ctx, http.MethodGet, "/api/v1/orgs/"+org.Slug+"/checks/stats", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	// Assert on the raw key set so a renamed or re-cased field is caught.
	var raw map[string]json.RawMessage

	r.NoError(json.Unmarshal(rec.Body.Bytes(), &raw))

	for _, key := range []string{"total", "enabled", "disabled", "byStatus", "down", "hardDown"} {
		r.Contains(raw, key, "response must carry the camelCase %q field", key)
	}

	// It is a stats object, not a check — proving /stats did not fall through
	// to the :checkUid route.
	r.NotContains(raw, "uid", "/checks/stats must not resolve to the :checkUid route")

	var payload checks.CheckStatsResponse

	r.NoError(json.Unmarshal(rec.Body.Bytes(), &payload))
	r.Equal(3, payload.Total)
	r.Equal(2, payload.Enabled)
	r.Equal(1, payload.Disabled)
	r.Equal(1, payload.Down)
	r.Equal(1, payload.HardDown)
	r.Equal(2, payload.ByStatus[models.WireStatusUp])
	r.Equal(1, payload.ByStatus[models.WireStatusDown])
}

// TestGetCheckStatsUnknownOrg returns 404 rather than an internal error.
func TestGetCheckStatsUnknownOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, _ := newStatsService(t, "stats-404")

	_, err := svc.GetCheckStats(t.Context(), "no-such-org")
	r.ErrorIs(err, checks.ErrOrganizationNotFound)
}

// TestGetCheckStatsCacheServesStaleThenRefreshes pins the documented caching
// contract from the outside: within the TTL a second call must serve the
// snapshot even though the underlying table has changed, and once the TTL has
// elapsed the next call must recompute. The TTL is exercised through the
// exported API with a real clock, using the shortest interval that is not
// flaky; the zero-allocation internal variant lives in stats_internal_test.go.
func TestGetCheckStatsCacheServesStaleThenRefreshes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := newStatsService(t, "stats-cache")
	checks.SetCheckStatsTTLForTest(svc, 150*time.Millisecond)

	seedStatsChecks(ctx, t, dbSvc, org.UID, []statsFixture{
		{status: models.CheckStatusUp, enabled: true},
	})

	first, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Equal(1, first.Total)

	// Mutate the table behind the cache's back.
	seedStatsChecks(ctx, t, dbSvc, org.UID, []statsFixture{
		{status: models.CheckStatusDown, enabled: true},
		{status: models.CheckStatusDown, enabled: true},
	})

	cached, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Equal(1, cached.Total, "within the TTL the cached snapshot must be served")
	r.Equal(0, cached.Down)

	time.Sleep(200 * time.Millisecond)

	fresh, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Equal(3, fresh.Total, "after the TTL elapses the stats must be recomputed")
	r.Equal(2, fresh.Down)
}

// TestGetCheckStatsCacheIsPerOrg makes sure one org's snapshot never leaks
// into another's — the cache is keyed by org UID.
func TestGetCheckStatsCacheIsPerOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, orgA := newStatsService(t, "stats-org-a")

	orgB := models.NewOrganization("stats-org-b", "Stats Org B")
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	seedStatsChecks(ctx, t, dbSvc, orgA.UID, []statsFixture{
		{status: models.CheckStatusUp, enabled: true},
		{status: models.CheckStatusDown, enabled: true},
	})
	seedStatsChecks(ctx, t, dbSvc, orgB.UID, []statsFixture{
		{status: models.CheckStatusUp, enabled: true},
	})

	statsA, err := svc.GetCheckStats(ctx, orgA.Slug)
	r.NoError(err)
	r.Equal(2, statsA.Total)
	r.Equal(1, statsA.Down)

	statsB, err := svc.GetCheckStats(ctx, orgB.Slug)
	r.NoError(err)
	r.Equal(1, statsB.Total)
	r.Equal(0, statsB.Down)
}

// TestGetCheckStatsCachedMapIsNotShared: mutating a returned ByStatus map must
// not corrupt the cached snapshot handed to the next caller.
func TestGetCheckStatsCachedMapIsNotShared(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := newStatsService(t, "stats-nomutate")
	seedStatsChecks(ctx, t, dbSvc, org.UID, []statsFixture{
		{status: models.CheckStatusUp, enabled: true},
	})

	first, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	first.ByStatus[models.WireStatusUp] = 9999

	second, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Equal(1, second.ByStatus[models.WireStatusUp])
}

// TestCreateCheckInvalidatesStatsCache is the load-bearing regression proof
// for spec 2026-09-01-01: a stats fetch primes the cache with total: 0, then
// CreateCheck must bust it so the very next fetch reports total: 1 without
// waiting out the (default, one-minute) TTL. No TTL override is used here —
// the default TTL is left in place on purpose, so a passing assertion can
// only mean the cache was genuinely busted, not that it happened to expire.
func TestCreateCheckInvalidatesStatsCache(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, _, org := newStatsService(t, "stats-create-bust")

	empty, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Equal(0, empty.Total, "a freshly primed cache for an empty org must read total: 0")

	_, err = svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)

	after, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Equal(1, after.Total,
		"creating a check must bust the cache so the next fetch sees it immediately, not after the TTL")
}

// TestDeleteCheckInvalidatesStatsCache is the symmetric case: deleting the
// org's only check must bust the cache immediately too, so a dashboard does
// not keep rendering the normal (non-empty) view over a now-empty org.
func TestDeleteCheckInvalidatesStatsCache(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, _, org := newStatsService(t, "stats-delete-bust")

	created, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)

	primed, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Equal(1, primed.Total, "the cache must be primed with the one check before it is deleted")

	r.NoError(svc.DeleteCheck(ctx, org.Slug, created.UID))

	after, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Equal(0, after.Total,
		"deleting a check must bust the cache so the next fetch sees the drop immediately, not after the TTL")
}

// TestCloneCheckInvalidatesStatsCache targets CloneCheck specifically: it
// bypasses Service.CreateCheck and inserts straight through
// insertCheckResolvingSlugRace, so this is the regression proof that the
// shared chokepoint really does cover it too, not just the CreateCheck path.
func TestCloneCheckInvalidatesStatsCache(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, _, org := newStatsService(t, "stats-clone-bust")

	created, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)

	primed, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Equal(1, primed.Total)

	_, err = svc.CloneCheck(ctx, org.Slug, created.UID, &checks.CloneCheckRequest{})
	r.NoError(err)

	after, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Equal(2, after.Total,
		"cloning a check must bust the cache too, since it bypasses CreateCheck")
}

// TestPlainResultWriteDoesNotInvalidateStatsCache is the control: it proves
// the TTL trade-off is preserved rather than the fix degenerating into
// "cache nothing". A plain result write changes nothing about which checks
// are in scope, so it must NOT bust the cache. Proven by mutating the checks
// table directly behind the cache's back (the same trick
// TestGetCheckStatsCacheServesStaleThenRefreshes uses) so a stale read below
// is unambiguous: only a cache that was never busted can produce it.
func TestPlainResultWriteDoesNotInvalidateStatsCache(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := newStatsService(t, "stats-result-no-bust")

	created, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)

	primed, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Equal(1, primed.Total)

	// Mutate the checks table behind the cache's back: if the cache were
	// somehow busted by the result write below, this second check would
	// show up in the recomputed total.
	seedStatsChecks(ctx, t, dbSvc, org.UID, []statsFixture{
		{status: models.CheckStatusUp, enabled: true},
	})

	result := models.NewResult(org.UID, created.UID, models.ResultStatusUp, 10)
	r.NoError(dbSvc.CreateResult(ctx, result))

	still, err := svc.GetCheckStats(ctx, org.Slug)
	r.NoError(err)
	r.Equal(1, still.Total, "a plain result write must not bust the stats cache")
}
