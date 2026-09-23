package statuspages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/statuspagekiosk"
)

// --- ParseViewOptions (unit, no DB) -----------------------------------------

// TestParseViewOptions pins the parsing rules the API contract depends on:
// absence vs presence-but-empty, trimming, dedup, case sensitivity and the
// unknown-token error (spec 2026-09-22-07).
func TestParseViewOptions(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		values     url.Values
		want       ViewOptions
		wantErrTok string // non-empty: expect an *InvalidIncludeError naming this token
	}{
		{
			name:   "absent means everything, the compatibility default",
			values: url.Values{},
			want:   AllViewOptions(),
		},
		{
			name:   "present but empty means neither",
			values: url.Values{"include": {""}},
			want:   ViewOptions{},
		},
		{
			name:   "availability alone",
			values: url.Values{"include": {"availability"}},
			want:   ViewOptions{Availability: true},
		},
		{
			name:   "responseTime alone",
			values: url.Values{"include": {"responseTime"}},
			want:   ViewOptions{ResponseTime: true},
		},
		{
			name:   "both, comma separated",
			values: url.Values{"include": {"availability,responseTime"}},
			want:   AllViewOptions(),
		},
		{
			name:   "order does not matter",
			values: url.Values{"include": {"responseTime,availability"}},
			want:   AllViewOptions(),
		},
		{
			name:   "duplicates are ignored",
			values: url.Values{"include": {"availability,availability"}},
			want:   ViewOptions{Availability: true},
		},
		{
			name:   "trailing comma leaves just the real token",
			values: url.Values{"include": {"availability,"}},
			want:   ViewOptions{Availability: true},
		},
		{
			name:   "surrounding whitespace is trimmed",
			values: url.Values{"include": {" availability , responseTime "}},
			want:   AllViewOptions(),
		},
		{
			name:       "unknown token is rejected",
			values:     url.Values{"include": {"foo"}},
			wantErrTok: "foo",
		},
		{
			name:       "case-sensitive: capitalized token is rejected",
			values:     url.Values{"include": {"Availability"}},
			wantErrTok: "Availability",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got, err := ParseViewOptions(testCase.values)

			if testCase.wantErrTok != "" {
				r.Error(err)

				var invalidErr *InvalidIncludeError
				r.ErrorAs(err, &invalidErr)
				r.Equal(testCase.wantErrTok, invalidErr.Token)
				r.Contains(err.Error(), testCase.wantErrTok)
				r.Contains(err.Error(), "availability")
				r.Contains(err.Error(), "responseTime")

				return
			}

			r.NoError(err)
			r.Equal(testCase.want, got)
		})
	}
}

// --- countingDB: a db.Service decorator counting the two expensive queries -

// countingDB wraps a real db.Service, counting calls to the two queries the
// availability enrichment issues, so a test can assert that `include=`
// short-circuits BEFORE either one runs rather than merely rendering an empty
// result from data it fetched anyway.
type countingDB struct {
	db.Service

	aggregateResultBucketsCalls int
	recentResultsPerCheckCalls  int

	// The view-fan-out counters, added by spec 2026-09-22-09's memo tests
	// (methods in memo_test.go). Atomic because those tests read them while a
	// request is in flight; the two counters above predate that and stay plain.
	orgLookups   atomic.Int64
	pageLookups  atomic.Int64
	sectionReads atomic.Int64
	resourceRead atomic.Int64
	updateReads  atomic.Int64
}

func (c *countingDB) AggregateResultBuckets(
	ctx context.Context, filter *models.ResultBucketFilter,
) ([]models.ResultBucket, error) {
	c.aggregateResultBucketsCalls++

	return c.Service.AggregateResultBuckets(ctx, filter)
}

func (c *countingDB) RecentResultsPerCheck(
	ctx context.Context, filter *models.RecentResultsPerCheckFilter,
) ([]*models.Result, error) {
	c.recentResultsPerCheckCalls++

	return c.Service.RecentResultsPerCheck(ctx, filter)
}

// setupIncludeTest stands up a 7-day page (the daily-bucket path, not the 24h
// hourly one) with availability AND response time enabled, one reporting
// check, wrapped in a countingDB so tests can assert on query counts.
func setupIncludeTest(t *testing.T) (context.Context, *Service, *countingDB, *models.Organization) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	sqliteDB, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(sqliteDB.Initialize(ctx))
	t.Cleanup(func() { _ = sqliteDB.Close() })

	counting := &countingDB{Service: sqliteDB}

	org := models.NewOrganization("acme", "Acme")
	r.NoError(sqliteDB.CreateOrganization(ctx, org))

	svc := NewService(counting, &config.Config{}, nil)

	showAvailability := true
	showResponseTime := true

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name:             "Public",
		Slug:             testPublicSlug,
		HistoryPeriod:    strPtr("7d"),
		ShowAvailability: &showAvailability,
		ShowResponseTime: &showResponseTime,
	})
	r.NoError(err)

	dropDefaultSections(ctx, t, svc, page.UID)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	seedCheckWithResults(ctx, t, svc, org, page.UID, section.UID, "Web", 10, 0)

	return ctx, svc, counting, org
}

// TestViewOptionsAbsentIsByteIdenticalToToday is the compatibility guarantee
// the whole feature rests on: parsing an empty query string (no `include` at
// all) must produce the exact same options — and therefore the exact same
// JSON — as the shape every consumer got before this parameter existed.
func TestViewOptionsAbsentIsByteIdenticalToToday(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, _, org := setupIncludeTest(t)

	parsedOpts, err := ParseViewOptions(url.Values{})
	r.NoError(err)

	viaParse, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug, parsedOpts)
	r.NoError(err)

	viaAll, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug, AllViewOptions())
	r.NoError(err)

	jsonParse, err := json.Marshal(viaParse)
	r.NoError(err)

	jsonAll, err := json.Marshal(viaAll)
	r.NoError(err)

	r.JSONEq(string(jsonAll), string(jsonParse))

	// And the golden shape itself: today's payload carries both sections.
	r.NotNil(viaAll.OverallAvailabilityPct)
	r.NotEmpty(viaAll.Sections)
	res := viaAll.Sections[0].Resources[0]
	r.NotNil(res.Availability)
	r.NotEmpty(res.Availability.DailyAvailability)
	r.NotEmpty(res.Availability.ResponseTimeSeries)
	r.NotNil(res.Availability.OverallAvailabilityPct)
}

// TestIncludeEmptyOmitsAvailabilityAndSkipsTheQueries is test #2: `include=`
// must not just render an empty availability block from data it fetched
// anyway — it must never issue either underlying query at all. This is the
// whole point of the feature (the two queries ARE the expensive half of the
// view).
func TestIncludeEmptyOmitsAvailabilityAndSkipsTheQueries(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, counting, org := setupIncludeTest(t)

	opts, err := ParseViewOptions(url.Values{"include": {""}})
	r.NoError(err)
	r.Equal(ViewOptions{}, opts)

	view, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug, opts)
	r.NoError(err)

	r.Nil(view.OverallAvailabilityPct)
	r.NotEmpty(view.Sections)

	for _, section := range view.Sections {
		for _, res := range section.Resources {
			r.Nil(res.Availability, "resource %q must carry no availability key at all", res.UID)
		}
	}

	// The raw JSON must not even carry the key, not just a null.
	raw, err := json.Marshal(view.Sections[0].Resources[0])
	r.NoError(err)
	r.NotContains(string(raw), `"availability"`)

	r.Zero(counting.aggregateResultBucketsCalls, "the bucket query must never run")
	r.Zero(counting.recentResultsPerCheckCalls, "the response-time query must never run")
}

// TestIncludeAvailabilityAloneOmitsSeries and its responseTime-alone sibling
// are test #3: each token turns on exactly its own half of the shape, mirroring
// buildAvailabilityData's existing showAvailability/showResponseTime split.
func TestIncludeAvailabilityAloneOmitsSeries(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, counting, org := setupIncludeTest(t)

	opts, err := ParseViewOptions(url.Values{"include": {"availability"}})
	r.NoError(err)

	view, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug, opts)
	r.NoError(err)

	r.NotNil(view.OverallAvailabilityPct)

	res := view.Sections[0].Resources[0]
	r.NotNil(res.Availability)
	r.NotEmpty(res.Availability.DailyAvailability)
	r.Empty(res.Availability.ResponseTimeSeries)

	r.Positive(counting.aggregateResultBucketsCalls, "the bucket query must run")
	r.Zero(counting.recentResultsPerCheckCalls, "the response-time query must NOT run")
}

func TestIncludeResponseTimeAloneOmitsDailyPoints(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, counting, org := setupIncludeTest(t)

	opts, err := ParseViewOptions(url.Values{"include": {"responseTime"}})
	r.NoError(err)

	view, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug, opts)
	r.NoError(err)

	// Page-level percentage is gated on ShowAvailability, which responseTime
	// alone must not turn on.
	r.Nil(view.OverallAvailabilityPct)

	res := view.Sections[0].Resources[0]
	r.NotNil(res.Availability)
	r.Empty(res.Availability.DailyAvailability)
	r.Nil(res.Availability.OverallAvailabilityPct)
	r.NotEmpty(res.Availability.ResponseTimeSeries)

	// The bucket query still runs — mergeBuckets/uptimebar hints feed the
	// availability bar computation that buildAvailabilityData then discards
	// via showAvailability=false — but the point of `include` is skipping it
	// only when NEITHER section wants it (test above); wanting one section
	// still enrichWithAvailability's shared per-resource loop.
	r.Positive(counting.recentResultsPerCheckCalls, "the response-time query must run")
}

// TestIncludeDedupAndOrderMatchTheDefault is test #4: every equivalent
// spelling of "both" — explicit list in either order, or a duplicated token —
// must produce the identical shape absence produces.
func TestIncludeDedupAndOrderMatchTheDefault(t *testing.T) {
	t.Parallel()

	ctx, svc, _, org := setupIncludeTest(t)

	baseline, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug, AllViewOptions())
	require.NoError(t, err)

	baselineJSON, err := json.Marshal(baseline)
	require.NoError(t, err)

	for _, raw := range []string{
		"availability,responseTime",
		"responseTime,availability",
		"availability,availability,responseTime",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			opts, err := ParseViewOptions(url.Values{"include": {raw}})
			r.NoError(err)
			r.Equal(AllViewOptions(), opts)

			view, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug, opts)
			r.NoError(err)

			viewJSON, err := json.Marshal(view)
			r.NoError(err)

			r.JSONEq(string(baselineJSON), string(viewJSON))
		})
	}
}

// --- HTTP layer: unknown token, gated cache, kiosk composition -------------

// newIncludeViewRequest builds a chi-routed GET request for the page-view
// handler with an arbitrary raw query string attached.
func newIncludeViewRequest(orgSlug, slug, rawQuery string) (*http.Request, *httptest.ResponseRecorder) {
	target := "/api/v1/status-pages/" + orgSlug + "/" + slug
	if rawQuery != "" {
		target += "?" + rawQuery
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org", orgSlug)
	rctx.URLParams.Add("slug", slug)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	// Every public read goes through the kiosk-grant middleware in production
	// (statusPageKioskGrant); reproduce that here so a kiosk-carrying query
	// behaves the same as it would through the real router.
	req = statuspagekiosk.WithRequestGrant(req)

	return req, httptest.NewRecorder()
}

// TestUnknownIncludeTokenIs400WithGatedCache is test #5: a malformed
// `include` is a client error naming the bad token, and — because it is
// answered before we know whether the page is even public — the response
// must carry the same never-shared-cache directive as a 404/401 would.
func TestUnknownIncludeTokenIs400WithGatedCache(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		query string
		token string
	}{
		{name: "unknown token", query: "include=foo", token: "foo"},
		{name: "wrong case is unknown too", query: "include=Availability", token: "Availability"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			_, svc, _, org := setupIncludeTest(t)

			h := NewHandler(svc, &config.Config{})
			req, rec := newIncludeViewRequest(org.Slug, testPublicSlug, testCase.query)

			r.NoError(h.ViewStatusPage(rec, req))

			resp := rec.Result()
			defer func() { _ = resp.Body.Close() }()

			r.Equal(http.StatusBadRequest, resp.StatusCode)
			r.Equal("private, no-store", resp.Header.Get("Cache-Control"))

			var body struct {
				Code  string `json:"code"`
				Title string `json:"title"`
			}
			r.NoError(json.NewDecoder(resp.Body).Decode(&body))
			r.Equal("VALIDATION_ERROR", body.Code)
			r.Contains(body.Title, testCase.token)
		})
	}
}

// TestNarrowingNeverWidens is test #6: `include=availability` on a page whose
// operator turned ShowAvailability off must stay off. The parameter can only
// narrow the page's own settings, never override them.
func TestNarrowingNeverWidens(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, _, org := setupIncludeTest(t)

	showAvailability := false
	_, err := svc.UpdateStatusPage(ctx, org.Slug, testPublicSlug, &UpdateStatusPageRequest{
		ShowAvailability: &showAvailability,
	})
	r.NoError(err)

	opts, err := ParseViewOptions(url.Values{"include": {"availability"}})
	r.NoError(err)

	view, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug, opts)
	r.NoError(err)

	r.False(view.ShowAvailability, "the page settings must still read false")
	r.Nil(view.OverallAvailabilityPct)

	for _, section := range view.Sections {
		for _, res := range section.Resources {
			r.Nil(res.Availability)
		}
	}
}

// TestKioskAndIncludeComposeEitherOrder is test #7: on a password-protected
// page, a valid kiosk token unlocks it and `include` narrows it regardless of
// which query parameter is written first.
func TestKioskAndIncludeComposeEitherOrder(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		queryFn func(token string) string
	}{
		{name: "kiosk then include", queryFn: func(token string) string {
			return "kiosk=" + token + "&include=availability"
		}},
		{name: "include then kiosk", queryFn: func(token string) string {
			return "include=availability&kiosk=" + token
		}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			ctx, svc, _, org := setupIncludeTest(t)

			visibility := models.StatusPageVisibilityPassword
			password := testPagePassword
			_, err := svc.UpdateStatusPage(ctx, org.Slug, testPublicSlug, &UpdateStatusPageRequest{
				Visibility: &visibility,
				Password:   &password,
			})
			r.NoError(err)

			minted := mintKioskToken(ctx, t, svc)

			h := NewHandler(svc, &config.Config{})
			req, rec := newIncludeViewRequest(org.Slug, testPublicSlug, testCase.queryFn(minted))

			r.NoError(h.ViewStatusPage(rec, req))

			resp := rec.Result()
			defer func() { _ = resp.Body.Close() }()

			r.Equal(http.StatusOK, resp.StatusCode, "a valid kiosk token must unlock the page")

			var view StatusPageResponse
			r.NoError(json.NewDecoder(resp.Body).Decode(&view))

			r.NotEmpty(view.Sections)
			res := view.Sections[0].Resources[0]
			r.NotNil(res.Availability, "include=availability must still be honored")
			r.NotEmpty(res.Availability.DailyAvailability)
			r.Empty(res.Availability.ResponseTimeSeries, "responseTime was not requested")
		})
	}
}
