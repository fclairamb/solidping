package middleware_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/errorreport/errorreporttest"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

var errHandlerExploded = errors.New("handler exploded")

const (
	sentryTestOrg    = "acmetech"
	sentryTestUser   = "alice@acme.com"
	sentryTestIssuer = "https://solidping.test"
)

// hubInjector stands in for "something upstream already put a hub on the
// context" — the branch SentryMiddleware takes when it finds one. It is what
// lets each test own its client and its events while still driving the real
// middleware, so t.Parallel() stays honest against Sentry's process-wide state.
func hubInjector(hub *sentry.Hub) httpx.Middleware {
	return func(next httpx.HandlerFunc) httpx.HandlerFunc {
		return func(writer http.ResponseWriter, req *http.Request) error {
			ctx := sentry.SetHubOnContext(req.Context(), hub.Clone())

			return next(writer, req.WithContext(ctx))
		}
	}
}

// sentryAuthFixture is a real auth stack over in-memory SQLite: real service,
// real JWT, real user row, so RequireAuth runs its actual code path.
type sentryAuthFixture struct {
	mw      *middleware.AuthMiddleware
	authSvc *auth.Service
	token   string
	user    *models.User
	db      db.Service
}

func newSentryAuthFixture(t *testing.T) *sentryAuthFixture {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	authCfg := config.AuthConfig{
		JWTSecret:          "test-jwt-secret",
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}
	fullCfg := &config.Config{Auth: authCfg, Server: config.ServerConfig{BaseURL: sentryTestIssuer}}
	authService := auth.NewService(dbSvc, authCfg, fullCfg, nil, nil)

	org := models.NewOrganization(sentryTestOrg, "Acme Tech")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser(sentryTestUser)
	pwd := "$plaintext$pw"
	user.PasswordHash = &pwd
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	loginResp, err := authService.Login(ctx, sentryTestOrg, sentryTestUser, "pw", auth.Context{})
	r.NoError(err)

	return &sentryAuthFixture{
		mw:      middleware.NewAuthMiddleware(authService, dbSvc, fullCfg),
		authSvc: authService,
		token:   loginResp.AccessToken,
		user:    user,
		db:      dbSvc,
	}
}

// mcpToken mints an audience-bound MCP access token for the fixture's user, the
// only kind RequireMCPAuth accepts.
func (f *sentryAuthFixture) mcpToken(t *testing.T) string {
	t.Helper()

	token, err := f.authSvc.GenerateMCPAccessToken(
		f.user.UID, sentryTestOrg, []string{"mcp"}, sentryTestIssuer+"/api/v1/mcp", time.Hour, "",
	)
	require.NoError(t, err)

	return token
}

// failingHandler writes a 500 the way every handler in the codebase does.
func failingHandler(writer http.ResponseWriter, req *http.Request) error {
	handlerBase := base.NewHandlerBase(nil)

	return handlerBase.WriteInternalError(writer, req, errHandlerExploded)
}

func panickingHandler(http.ResponseWriter, *http.Request) error {
	panic("simulated handler panic")
}

// eventText is what the event says happened, whichever shape Sentry chose: a
// panic with a string value becomes a message, one with an error becomes an
// exception.
func eventText(event *sentry.Event) string {
	parts := make([]string, 0, len(event.Exception)+1)
	parts = append(parts, event.Message)

	for _, exception := range event.Exception {
		parts = append(parts, exception.Value)
	}

	return strings.Join(parts, " | ")
}

// response is the fully-consumed answer to one test request.
type response struct {
	status int
	header http.Header
	body   []byte
}

// serve runs one request through a router carrying the given chain, using a
// real httptest server so chi's route pattern (and therefore the HTTP metrics)
// is populated exactly as in production.
func serve(
	t *testing.T, pattern string, chain []httpx.Middleware, handler httpx.HandlerFunc, token string,
) response {
	t.Helper()

	r := require.New(t)

	router := httpx.New()
	group := &router.Group

	for _, mw := range chain {
		group = group.Use(mw)
	}

	group.GET(pattern, handler)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+pattern, http.NoBody)
	r.NoError(err)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	return response{status: resp.StatusCode, header: resp.Header.Clone(), body: body}
}

// TestSentryMiddleware_AuthenticatedRequestCarriesUserAndOrg drives the real
// SentryMiddleware -> Recoverer -> RequireAuth chain. Before this fix
// SentryMiddleware read the claims itself, at a point in the chain where they
// provably did not exist yet, so every event was anonymous.
func TestSentryMiddleware_AuthenticatedRequestCarriesUserAndOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fixture := newSentryAuthFixture(t)

	hub, transport, err := errorreporttest.NewHub()
	r.NoError(err)

	resp := serve(t, "/api/v1/orgs/:org/authed-boom",
		[]httpx.Middleware{
			hubInjector(hub),
			middleware.SentryMiddleware(),
			middleware.Recoverer,
			fixture.mw.RequireAuth,
		},
		failingHandler, fixture.token)

	r.Equal(http.StatusInternalServerError, resp.status)

	events := transport.Events()
	r.Len(events, 1)
	r.Equal(fixture.user.UID, events[0].User.ID)
	r.Equal(sentryTestOrg, events[0].Tags["organization"])
	r.NotNil(events[0].Request, "SentryMiddleware must attach the request to the scope")
	r.Equal("GET", events[0].Request.Method)
}

// Positive control for the test above: the same handler on a route with no
// RequireAuth still reports, but with no identity attached. If the assertions
// above passed for the wrong reason (e.g. against a hard-coded scope), this
// would pass too — it does not.
func TestSentryMiddleware_UnauthenticatedRequestCarriesNoIdentity(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hub, transport, err := errorreporttest.NewHub()
	r.NoError(err)

	resp := serve(t, "/api/v1/public-boom",
		[]httpx.Middleware{
			hubInjector(hub),
			middleware.SentryMiddleware(),
			middleware.Recoverer,
		},
		failingHandler, "")

	r.Equal(http.StatusInternalServerError, resp.status)

	events := transport.Events()
	r.Len(events, 1)
	r.Empty(events[0].User.ID, "an anonymous request must not carry a user")
	r.NotContains(events[0].Tags, "organization")
}

// TestRecoverer_PanicIs500JSONAndExactlyOneEvent pins requirements 1 and 2 of
// the recovery middleware: the client gets the standard 500 body instead of a
// dropped connection, and the panic yields exactly one event — carrying the
// request scope and the identity RequireAuth attached.
func TestRecoverer_PanicIs500JSONAndExactlyOneEvent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fixture := newSentryAuthFixture(t)

	hub, transport, err := errorreporttest.NewHub()
	r.NoError(err)

	resp := serve(t, "/api/v1/orgs/:org/panic",
		[]httpx.Middleware{
			hubInjector(hub),
			middleware.SentryMiddleware(),
			middleware.Recoverer,
			fixture.mw.RequireAuth,
		},
		panickingHandler, fixture.token)

	r.Equal(http.StatusInternalServerError, resp.status)
	r.Equal("application/json", resp.header.Get("Content-Type"))

	var body base.ErrorResponse

	r.NoError(json.Unmarshal(resp.body, &body))
	r.Equal(string(base.ErrorCodeInternalError), body.Code)
	r.Equal("Internal server error", body.Title)

	events := transport.Events()
	r.Len(events, 1, "exactly one component may report a panic")
	r.Contains(eventText(events[0]), "simulated handler panic")
	r.Equal(fixture.user.UID, events[0].User.ID)
	r.Equal(sentryTestOrg, events[0].Tags["organization"])
}

// Positive control: the identical chain with a handler that does not panic
// produces a 200 and no events at all.
func TestRecoverer_HealthyHandlerProducesNoEvent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hub, transport, err := errorreporttest.NewHub()
	r.NoError(err)

	resp := serve(t, "/api/v1/quiet",
		[]httpx.Middleware{
			hubInjector(hub),
			middleware.SentryMiddleware(),
			middleware.Recoverer,
		},
		func(writer http.ResponseWriter, _ *http.Request) error {
			writer.WriteHeader(http.StatusOK)

			return nil
		}, "")

	r.Equal(http.StatusOK, resp.status)
	r.Equal(0, transport.Len())
}

// TestRecoverer_PanicIsLoggedAndCountedAsAnOrdinary500 pins requirement 3: the
// logging and HTTP-metrics middleware sit OUTSIDE the recoverer, so a panicking
// request is a normal failed request to them — an error they can log and a 500
// in /metrics — rather than a hole in both.
func TestRecoverer_PanicIsLoggedAndCountedAsAnOrdinary500(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hub, _, err := errorreporttest.NewHub()
	r.NoError(err)

	const pattern = "/api/v1/panic-metrics"

	counter, err := prommetrics.HTTPRequestsTotal.GetMetricWithLabelValues(
		http.MethodGet, pattern, "500")
	r.NoError(err)

	before := testutil.ToFloat64(counter)

	var observed error

	// Stands in for s.loggingMiddleware, which logs whatever error the chain
	// hands back to it.
	logging := func(next httpx.HandlerFunc) httpx.HandlerFunc {
		return func(writer http.ResponseWriter, req *http.Request) error {
			observed = next(writer, req)

			return observed
		}
	}

	resp := serve(t, pattern,
		[]httpx.Middleware{
			hubInjector(hub),
			middleware.SentryMiddleware(),
			logging,
			middleware.HTTPMetrics,
			middleware.Recoverer,
		},
		panickingHandler, "")

	r.Equal(http.StatusInternalServerError, resp.status)
	r.ErrorIs(observed, middleware.ErrPanic, "the logging middleware must see a failed request, not a panic")
	r.InDelta(before+1, testutil.ToFloat64(counter), 0.001,
		"the panicking request must be counted as a 500 in /metrics")
}

// http.ErrAbortHandler keeps its stdlib meaning: abort silently. It must not be
// turned into a 500 body and must never reach Sentry.
func TestRecoverer_ErrAbortHandlerIsNotReported(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hub, transport, err := errorreporttest.NewHub()
	r.NoError(err)

	router := httpx.New()
	group := router.Use(hubInjector(hub)).Use(middleware.SentryMiddleware()).Use(middleware.Recoverer)
	group.GET("/api/v1/abort", func(http.ResponseWriter, *http.Request) error {
		panic(http.ErrAbortHandler)
	})

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/abort", http.NoBody)
	r.NoError(err)

	//nolint:bodyclose // the connection is aborted server-side; there is no body to close
	_, doErr := http.DefaultClient.Do(req)
	r.Error(doErr, "ErrAbortHandler aborts the response, it does not answer 500")
	r.Equal(0, transport.Len())
}

// TestRecoverer_CatchesPanicsUnderTheRequestTimeout covers the sharpest
// property of the recoverer itself: RequestTimeout runs the handler on its own
// goroutine, and recover() only catches panics on the goroutine it runs on, so
// a recoverer must be able to work from *inside* the timeout. Mounted the other
// way round it catches nothing and the panic takes the whole process down —
// which is why the wrong order here does not fail politely, it crashes the test
// binary.
//
// What this does NOT guard: the chain this test builds is written by hand, so
// it keeps passing no matter how internal/app registers its middleware. The
// production order is pinned separately, by
// app.TestBuildMainGroup_PanickingRouteIs500ReportedOnceAndCounted, which drives
// a panic through the real buildMainGroup.
func TestRecoverer_CatchesPanicsUnderTheRequestTimeout(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hub, transport, err := errorreporttest.NewHub()
	r.NoError(err)

	resp := serve(t, "/api/v1/timed-panic",
		[]httpx.Middleware{
			hubInjector(hub),
			middleware.SentryMiddleware(),
			middleware.HTTPMetrics,
			middleware.RequestTimeout(30 * time.Second),
			middleware.Recoverer,
		},
		panickingHandler, "")

	r.Equal(http.StatusInternalServerError, resp.status)
	r.Len(transport.Events(), 1)
}

// RequireMCPAuth attaches the identity through the same helper as RequireAuth,
// on a path RequireAuth never runs. Without this, deleting that one line from
// the MCP gate would break nothing in the suite while quietly making every MCP
// event anonymous again.
func TestRequireMCPAuth_AuthenticatedRequestCarriesUserAndOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fixture := newSentryAuthFixture(t)

	hub, transport, err := errorreporttest.NewHub()
	r.NoError(err)

	resp := serve(t, "/api/v1/mcp-boom",
		[]httpx.Middleware{
			hubInjector(hub),
			middleware.SentryMiddleware(),
			middleware.Recoverer,
			fixture.mw.RequireMCPAuth,
		},
		failingHandler, fixture.mcpToken(t))

	r.Equal(http.StatusInternalServerError, resp.status)

	events := transport.Events()
	r.Len(events, 1)
	r.Equal(fixture.user.UID, events[0].User.ID)
	r.Equal(sentryTestOrg, events[0].Tags["organization"])
}
