package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// testModeGateServer boots a real server (real NewServer + real SetupRoutes)
// over an in-memory SQLite DB with the given RunMode, mirroring
// telegramRouteServer in telegram_route_test.go.
func testModeGateServer(t *testing.T, runMode string) *httptest.Server {
	t.Helper()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "testapi-gate-secret"
	cfg.RunMode = runMode

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	return ts
}

// gatedRoute is one route registered inside the RunMode=="test" block.
type gatedRoute struct {
	name   string
	method string
	path   string
}

// testModeGatedRoutes lists every route server.go registers inside the
// `if s.config.RunMode == "test"` block (spec 2026-08-23-07). Add a route
// here whenever one is added there: TestTestModeRoutesGatedByRunMode covers
// the whole block, not only /test/jobs, because an ungated route in this
// block is reachable, unauthenticated, in every production deployment — the
// exact bug this spec fixes for /test/jobs — and the failure mode is silent
// and identical for every sibling route.
//
//nolint:gochecknoglobals // read-only test-data table, not mutable state.
var testModeGatedRoutes = []gatedRoute{
	{"create email job", http.MethodPost, "/api/v1/test/jobs"},
	{"list state entries", http.MethodGet, "/api/v1/test/state-entries"},
	{"create user", http.MethodPost, "/api/v1/test/users"},
	{"bulk create checks", http.MethodPost, "/api/v1/test/checks/bulk"},
	{"bulk delete checks", http.MethodDelete, "/api/v1/test/checks/bulk"},
	{"generate data", http.MethodPost, "/api/v1/test/generate-data"},
	{"delete all checks", http.MethodDelete, "/api/v1/test/checks/all"},
	{"email preview", http.MethodGet, "/api/mgmt/email-preview/welcome.html"},
}

// TestTestModeRoutesGatedByRunMode is the positive+negative control for the
// RunMode=="test" gate: every route in testModeGatedRoutes must be
// unreachable outside test mode, and must NOT be unreachable when
// RunMode=="test" (i.e. must actually be registered there). "Unreachable"
// means 404 or 405: the SPA catch-all is registered GET-only at "/*path", so
// an unregistered POST/DELETE path collides with it on path (not method) and
// chi answers 405 rather than 404 — TestRouteMatchingPrecedence documents the
// same "post to unknown path is 405 not 404" behavior, and
// TestTelegramWebhookRouteAbsentWithoutToken accepts both for the identical
// reason. Either status proves the sensitive handler never ran. Checking
// only the "must be unreachable" direction would pass on a gate that was
// accidentally left permanently closed (a typo'd condition, or the block
// moved somewhere it's never reached) without anyone noticing the dev/test
// workflow the routes exist for was silently broken.
func TestTestModeRoutesGatedByRunMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"", "test"} {
		t.Run(fmt.Sprintf("RunMode=%q", mode), func(t *testing.T) {
			t.Parallel()

			ts := testModeGateServer(t, mode)

			for _, route := range testModeGatedRoutes {
				t.Run(route.name, func(t *testing.T) {
					t.Parallel()
					r := require.New(t)

					req, err := http.NewRequestWithContext(t.Context(), route.method, ts.URL+route.path, http.NoBody)
					r.NoError(err)

					resp, err := ts.Client().Do(req)
					r.NoError(err)

					defer func() { _ = resp.Body.Close() }()

					if mode == "test" {
						r.NotContains([]int{http.StatusNotFound, http.StatusMethodNotAllowed}, resp.StatusCode,
							"route %s %s must be registered when RunMode=test", route.method, route.path)
					} else {
						r.Contains([]int{http.StatusNotFound, http.StatusMethodNotAllowed}, resp.StatusCode,
							"route %s %s must be unreachable outside test mode", route.method, route.path)
					}
				})
			}
		})
	}
}

// TestFakeRouteStaysPublicOutsideTestMode is the negative control for
// requirement 2 of spec 2026-08-23-07: unlike every route in
// testModeGatedRoutes, GET /api/v1/fake is deliberately public product
// surface (live dashboard callers, potentially customer checks) and must
// stay reachable with RunMode=="" — it must not get swept into the test-mode
// gate by a future refactor that "cleans up" the registration block.
func TestFakeRouteStaysPublicOutsideTestMode(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ts := testModeGateServer(t, "")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/fake?statusDown=200", http.NoBody)
	r.NoError(err)

	resp, err := ts.Client().Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
}

// TestFakeRouteIsRateLimited proves the dedicated /fake limiter (spec
// 2026-08-23-07's bound on the connection-holding vector) is actually wired
// into the real route, not just defined and unused: firing more requests
// than fakeAPIRequestsPerMinute's burst allows from one IP must eventually
// draw a 429, well before the general per-IP budget (which this test's
// zero-value config disables entirely) would ever kick in.
func TestFakeRouteIsRateLimited(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ts := testModeGateServer(t, "")

	sawTooManyRequests := false

	burst := fakeAPIRequestsPerMinute / 5

	for range burst + 5 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/fake?statusDown=200", http.NoBody)
		r.NoError(err)

		resp, err := ts.Client().Do(req)
		r.NoError(err)
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			sawTooManyRequests = true

			break
		}
	}

	r.True(sawTooManyRequests, "expected /fake to be rate-limited within burst+5 rapid requests")
}
