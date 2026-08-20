package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/errorreport/errorreporttest"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// TestBuildMainGroup_PanickingRouteIs500ReportedOnceAndCounted drives a panic
// through the REAL global middleware chain — the one buildMainGroup installs
// for every route in server.go — rather than a hand-assembled imitation of it.
// That is the difference that matters: the middleware-level tests in
// internal/middleware prove Recoverer behaves correctly in a chain someone
// writes by hand, and would keep passing if production's registration were
// reordered. This one moves with server.go.
//
// It binds the process-wide Sentry client instead of seeding a hub on the
// context, so SentryMiddleware takes its sentry.CurrentHub().Clone() branch —
// again, the one production actually takes.
//
// Three properties, all from the spec's part C:
//  1. the client gets a 500 with the standard JSON body, not a dropped
//     connection (and not a process crash: RequestTimeout runs the handler on
//     its own goroutine, so a recoverer mounted above it would catch nothing);
//  2. exactly one Sentry event;
//  3. the logging and metrics middleware, which sit outside the recoverer, see
//     an ordinary 500 and count it.
//
//nolint:paralleltest // binds the process-wide Sentry client
func TestBuildMainGroup_PanickingRouteIs500ReportedOnceAndCounted(t *testing.T) {
	r := require.New(t)

	client, transport, err := errorreporttest.NewClient()
	r.NoError(err)

	previous := sentry.CurrentHub().Client()
	sentry.CurrentHub().BindClient(client)

	t.Cleanup(func() { sentry.CurrentHub().BindClient(previous) })

	const pattern = "/api/v1/orgs/:org/chain-panic-probe"

	cfg := &config.Config{}
	cfg.Server.MaxRequestDuration = 30 * time.Second

	srv := &Server{config: cfg}

	router := httpx.New()
	mainGroup := srv.buildMainGroup(t.Context(), router)
	mainGroup.GET(pattern, func(http.ResponseWriter, *http.Request) error {
		panic("simulated handler panic in the real chain")
	})

	// chi reports the pattern in its own syntax, which is what the metric is
	// labeled with.
	counter, err := prommetrics.HTTPRequestsTotal.GetMetricWithLabelValues(
		http.MethodGet, "/api/v1/orgs/{org}/chain-panic-probe", strconv.Itoa(http.StatusInternalServerError))
	r.NoError(err)

	before := testutil.ToFloat64(counter)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, server.URL+"/api/v1/orgs/acme/chain-panic-probe", http.NoBody)
	r.NoError(err)

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	r.NoError(err)

	r.Equal(http.StatusInternalServerError, resp.StatusCode,
		"a panicking handler must answer 500, not reset the connection")
	r.Equal("application/json", resp.Header.Get("Content-Type"))

	var body base.ErrorResponse

	r.NoError(json.Unmarshal(raw, &body))
	r.Equal(string(base.ErrorCodeInternalError), body.Code)
	r.Equal("Internal server error", body.Title)

	events := transport.Events()
	r.Len(events, 1, "exactly one component in the real chain may report a panic")

	r.InDelta(before+1, testutil.ToFloat64(counter), 0.001,
		"HTTPMetrics sits outside the recoverer and must count the panic as a 500")
}
