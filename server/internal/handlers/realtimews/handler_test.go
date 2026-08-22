package realtimews_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/realtimews"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/realtime"
)

// wsFixture wires a real HTTP server around the realtimews handler so the
// WebSocket upgrade, handshake, and framing are exercised end-to-end.
type wsFixture struct {
	dbSvc       db.Service
	authService *auth.Service
	bus         *notifier.LocalEventNotifier
	hub         *realtime.Hub
	pub         *realtime.Publisher
	server      *httptest.Server
	wsURL       string
}

// wsFixtureOpts allows individual tests to tweak the realtime config (e.g.
// disable the feature, lower the subscription cap).
type wsFixtureOpts struct {
	disabled         bool
	maxSubscriptions int
	pingInterval     time.Duration
}

func newWSFixture(t *testing.T, opts wsFixtureOpts) *wsFixture {
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
	// fullCfg must be non-nil: the auth Service reads JWTSecret live via
	// fullCfg.Auth when it mints/validates the session tokens this fixture uses.
	authService := auth.NewService(dbSvc, authCfg, &config.Config{Auth: authCfg}, nil, nil)

	if opts.pingInterval <= 0 {
		opts.pingInterval = time.Minute
	}

	cfg := &config.Config{
		Auth: authCfg,
		Realtime: config.RealtimeConfig{
			Enabled:                       !opts.disabled,
			FlushInterval:                 50 * time.Millisecond,
			PingInterval:                  opts.pingInterval,
			MaxConnections:                0,
			MaxSubscriptionsPerConnection: opts.maxSubscriptions,
		},
	}

	bus := notifier.NewLocalEventNotifier()
	hub := realtime.NewHubWithSubscriptionCap(
		bus, cfg.Realtime.MaxConnections, cfg.Realtime.MaxSubscriptionsPerConnection, nil)
	pub := realtime.NewPublisher(ctx, bus, cfg.Realtime.FlushInterval, nil)

	handler := realtimews.NewHandler(hub, authService, dbSvc, cfg)

	router := httpx.New()
	router.GET("/api/v1/orgs/:org/events/ws", func(w http.ResponseWriter, req *http.Request) error {
		return handler.Serve(w, req)
	})
	server := httptest.NewServer(router)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/orgs/test/events/ws"

	fx := &wsFixture{
		dbSvc: dbSvc, authService: authService, bus: bus, hub: hub, pub: pub, server: server, wsURL: wsURL,
	}
	t.Cleanup(func() {
		server.Close()
		pub.Close()
		hub.Close()
		_ = bus.Close()
	})

	return fx
}

// seedOrgAndUser creates an org, a member user with a plaintext password, and
// returns (org, checkFactory). checkFactory creates a check in that org.
func (fx *wsFixture) seedOrgAndUser(t *testing.T) *models.Organization {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	org := models.NewOrganization("test", "Test Org")
	r.NoError(fx.dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("member@example.com")
	pwd := "$plaintext$pw"
	user.PasswordHash = &pwd
	r.NoError(fx.dbSvc.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	r.NoError(fx.dbSvc.CreateOrganizationMember(ctx, member))

	return org
}

func (fx *wsFixture) createCheck(t *testing.T, org *models.Organization) *models.Check {
	t.Helper()
	r := require.New(t)

	check := models.NewCheck(org.UID, "api-"+time.Now().Format("150405.000000000"), "http")
	r.NoError(fx.dbSvc.CreateCheck(t.Context(), check))

	return check
}

// token mints a real access token for the seeded member.
func (fx *wsFixture) token(t *testing.T) string {
	t.Helper()
	r := require.New(t)

	resp, err := fx.authService.Login(t.Context(), "test", "member@example.com", "pw", auth.Context{})
	r.NoError(err)

	return resp.AccessToken
}

// dialExpect401 attempts an upgrade with the given dial options and asserts the
// handshake is rejected with an HTTP 401 BEFORE any upgrade — no socket. Token
// authentication now runs at the HTTP level, so coder/websocket surfaces the
// non-101 response on (err, resp) rather than upgrading and closing.
func (fx *wsFixture) dialExpect401(t *testing.T, opts *websocket.DialOptions) {
	t.Helper()
	r := require.New(t)

	conn, resp, err := websocket.Dial(t.Context(), fx.wsURL, opts)
	if conn != nil {
		_ = conn.CloseNow()
	}
	r.Error(err)
	r.NotNil(resp)
	if resp.Body != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	r.Equal(http.StatusUnauthorized, resp.StatusCode)
}

// dialPreAuth opens a connection with an Authorization header (the
// CLI/test/websocat path — authenticated at upgrade time).
func (fx *wsFixture) dialPreAuth(t *testing.T, token string) *websocket.Conn {
	t.Helper()
	r := require.New(t)

	conn, resp, err := websocket.Dial(t.Context(), fx.wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	r.NoError(err)
	if resp != nil && resp.Body != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	return conn
}

// dialWithCookie opens a connection carrying an access_token cookie — the
// shape a browser sends automatically (the login/refresh endpoints set this
// cookie for the unrelated OAuth-consent flow), as opposed to an explicit
// Authorization header a CLI client sets deliberately.
func (fx *wsFixture) dialWithCookie(t *testing.T, cookieToken string) *websocket.Conn {
	t.Helper()
	r := require.New(t)

	conn, resp, err := websocket.Dial(t.Context(), fx.wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{"access_token=" + cookieToken}},
	})
	r.NoError(err)
	if resp != nil && resp.Body != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	return conn
}

func readJSON[T any](t *testing.T, conn *websocket.Conn) T {
	t.Helper()
	r := require.New(t)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, data, err := conn.Read(ctx)
	r.NoError(err)

	var v T
	r.NoError(json.Unmarshal(data, &v))

	return v
}

// frameReader is a single background reader over a *websocket.Conn — reads
// must never run concurrently (coder/websocket's contract), and a deadline
// context passed straight into Read closes the connection as a side effect
// on expiry (see the Conn doc comment), so a "nothing arrived within window"
// probe cannot simply retry Read with a short timeout. Instead one goroutine
// owns the connection's reads for the test's lifetime and publishes frames
// onto a channel that both expectNoMessage and readNextJSON drain.
type frameReader struct {
	frames chan []byte
}

func newFrameReader(t *testing.T, conn *websocket.Conn) *frameReader {
	t.Helper()

	fr := &frameReader{frames: make(chan []byte, 16)}
	go func() {
		for {
			_, data, err := conn.Read(t.Context())
			if err != nil {
				close(fr.frames)

				return
			}
			fr.frames <- data
		}
	}()

	return fr
}

func (fr *frameReader) next(t *testing.T) []byte {
	t.Helper()

	select {
	case data, ok := <-fr.frames:
		if !ok {
			t.Fatal("connection closed while waiting for a frame")
		}

		return data
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a frame")

		return nil
	}
}

func (fr *frameReader) nextJSON(t *testing.T, v any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(fr.next(t), v))
}

// expectNoMessage asserts that no frame arrives within window.
func (fr *frameReader) expectNoMessage(t *testing.T, window time.Duration) {
	t.Helper()

	select {
	case data, ok := <-fr.frames:
		if !ok {
			t.Fatal("connection closed while expecting silence")
		}
		t.Fatalf("expected no message within %s, got %q", window, string(data))
	case <-time.After(window):
	}
}

func writeJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	r := require.New(t)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	data, err := json.Marshal(v)
	r.NoError(err)
	r.NoError(conn.Write(ctx, websocket.MessageText, data))
}

type helloFrame struct {
	Type     string `json:"type"`
	Protocol int    `json:"protocol"`
}

type subscribedFrame struct {
	Type   string `json:"type"`
	Entity string `json:"entity"`
	UID    string `json:"uid,omitempty"`
}

type updateFrame struct {
	Type   string   `json:"type"`
	Entity string   `json:"entity"`
	UID    string   `json:"uid,omitempty"`
	Kinds  []string `json:"kinds,omitempty"`
}

type errorFrame struct {
	Type   string `json:"type"`
	Code   string `json:"code"`
	Title  string `json:"title"`
	Entity string `json:"entity,omitempty"`
	UID    string `json:"uid,omitempty"`
}

func TestServe_PreAuthHeaderThenHello(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	org := fx.seedOrgAndUser(t)
	token := fx.token(t)

	conn := fx.dialPreAuth(t, token)
	hello := readJSON[helloFrame](t, conn)
	r.Equal("hello", hello.Type)
	r.Equal(realtimews.ProtocolVersion, hello.Protocol)

	_ = org
}

// TestServe_ValidCookieAuthenticatesImmediately covers the browser path: an
// access_token cookie (the browser attaches it to the same-origin upgrade
// automatically) authenticates at the HTTP level and the socket gets `hello`,
// same as a valid Authorization header.
func TestServe_ValidCookieAuthenticatesImmediately(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)
	token := fx.token(t)

	conn := fx.dialWithCookie(t, token)
	hello := readJSON[helloFrame](t, conn)
	r.Equal("hello", hello.Type)
}

// TestServe_MissingTokenRejectedWith401 pins the "explicit auth gets an answer"
// guarantee: with no Authorization header and no access_token cookie the
// handshake is answered with HTTP 401 before any upgrade (no socket).
func TestServe_MissingTokenRejectedWith401(t *testing.T) {
	t.Parallel()

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)

	fx.dialExpect401(t, nil)
}

// TestServe_InvalidHeaderTokenRejectedWith401 covers a deliberately-set but
// invalid Authorization header: answered at the HTTP level with 401, no upgrade
// (this was a post-upgrade 4401 close before this spec).
func TestServe_InvalidHeaderTokenRejectedWith401(t *testing.T) {
	t.Parallel()

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)

	fx.dialExpect401(t, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer not-a-valid-token"}},
	})
}

// TestServe_InvalidCookieTokenRejectedWith401 covers a stale/invalid
// access_token cookie: it now simply fails at the HTTP layer (401) instead of
// falling through to an in-band auth message, which no longer exists.
func TestServe_InvalidCookieTokenRejectedWith401(t *testing.T) {
	t.Parallel()

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)

	fx.dialExpect401(t, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{"access_token=stale-invalid-cookie-token"}},
	})
}

// TestServe_HeaderTokenWinsOverCookie pins precedence: a valid Authorization
// header alongside an invalid cookie authenticates via the header (mirrors
// middleware.extractToken — the header wins).
func TestServe_HeaderTokenWinsOverCookie(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)
	token := fx.token(t)

	conn, resp, err := websocket.Dial(t.Context(), fx.wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + token},
			"Cookie":        []string{"access_token=garbage-cookie-token"},
		},
	})
	r.NoError(err)
	if resp != nil && resp.Body != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	hello := readJSON[helloFrame](t, conn)
	r.Equal("hello", hello.Type)
}

// TestServe_SubprotocolBearerAuthenticates covers the SPA browser path: the
// access token rides in a bearer.* Sec-WebSocket-Protocol entry (browsers
// cannot set an Authorization header on a WebSocket) and the server negotiates
// the plain solidping.v2 entry back — without that echo a browser aborts the
// connection.
func TestServe_SubprotocolBearerAuthenticates(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)
	token := fx.token(t)

	conn, resp, err := websocket.Dial(t.Context(), fx.wsURL, &websocket.DialOptions{
		Subprotocols: []string{"bearer." + token, "solidping.v2"},
	})
	r.NoError(err)
	if resp != nil && resp.Body != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	r.Equal("solidping.v2", conn.Subprotocol(),
		"the server must negotiate the plain subprotocol back or a browser aborts the connection")

	hello := readJSON[helloFrame](t, conn)
	r.Equal("hello", hello.Type)
}

// TestServe_SubprotocolBearerWinsOverForeignCookie is the regression test for
// the localhost foreign-cookie collision: cookies ignore ports, so another
// app on the same host can plant its own access_token cookie (observed with
// iss=realassets) that permanently 401s a cookie-authenticated handshake while
// REST — authenticated by header — keeps working. The bearer.* subprotocol
// entry must win over the (foreign, invalid) cookie.
func TestServe_SubprotocolBearerWinsOverForeignCookie(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)
	token := fx.token(t)

	conn, resp, err := websocket.Dial(t.Context(), fx.wsURL, &websocket.DialOptions{
		Subprotocols: []string{"bearer." + token, "solidping.v2"},
		HTTPHeader:   http.Header{"Cookie": []string{"access_token=foreign-apps-invalid-token"}},
	})
	r.NoError(err)
	if resp != nil && resp.Body != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	hello := readJSON[helloFrame](t, conn)
	r.Equal("hello", hello.Type)
}

// TestServe_InvalidSubprotocolTokenRejectedWith401 covers an invalid token in
// the bearer.* subprotocol entry: rejected at the HTTP level with 401 (no
// silent fall-through to the cookie).
func TestServe_InvalidSubprotocolTokenRejectedWith401(t *testing.T) {
	t.Parallel()

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)

	fx.dialExpect401(t, &websocket.DialOptions{
		Subprotocols: []string{"bearer." + "not-a-valid-token", "solidping.v2"},
	})
}

// TestServe_HeaderWinsOverSubprotocol pins precedence: a present-but-invalid
// Authorization header is rejected even when a valid bearer.* subprotocol
// entry is offered (mirrors middleware.extractToken — a malformed header never
// falls back).
func TestServe_HeaderWinsOverSubprotocol(t *testing.T) {
	t.Parallel()

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)
	token := fx.token(t)

	fx.dialExpect401(t, &websocket.DialOptions{
		Subprotocols: []string{"bearer." + token, "solidping.v2"},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer not-a-valid-token"}},
	})
}

// TestServe_FirstMessageAuthFrameIsNotAuthentication pins the removal of the
// in-band auth path: the socket is already authenticated at the HTTP level, so
// a first-message {"type":"auth"} frame is treated as any other frame — it hits
// the default "Unknown message type" branch and the socket stays open.
func TestServe_FirstMessageAuthFrameIsNotAuthentication(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)
	token := fx.token(t)

	conn := fx.dialPreAuth(t, token)
	_ = readJSON[helloFrame](t, conn)

	writeJSON(t, conn, map[string]string{"type": "auth", "token": token})
	errFrame := readJSON[errorFrame](t, conn)
	r.Equal("error", errFrame.Type)
	r.Equal("VALIDATION_ERROR", errFrame.Code)

	// Socket stays open: a valid subscribe still works afterward.
	writeJSON(t, conn, map[string]string{"type": "subscribe", "entity": "checks"})
	sub := readJSON[subscribedFrame](t, conn)
	r.Equal("subscribed", sub.Type)
}

func TestServe_ForeignOrgClosesForbidden(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	// Seed a user in a *different* org than the one the socket connects to
	// ("test", baked into wsURL); the member's own org is "other".
	org := models.NewOrganization("other", "Other Org")
	r.NoError(fx.dbSvc.CreateOrganization(t.Context(), org))
	user := models.NewUser("outsider@example.com")
	pwd := "$plaintext$pw"
	user.PasswordHash = &pwd
	r.NoError(fx.dbSvc.CreateUser(t.Context(), user))
	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	r.NoError(fx.dbSvc.CreateOrganizationMember(t.Context(), member))

	resp, err := fx.authService.Login(t.Context(), "other", "outsider@example.com", "pw", auth.Context{})
	r.NoError(err)

	conn := fx.dialPreAuth(t, resp.AccessToken)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, _, readErr := conn.Read(ctx)
	r.Error(readErr)
	r.Equal(realtimews.CloseForbidden, websocket.CloseStatus(readErr))
}

// TestServe_DBSuperAdminForeignOrgClosesForbidden locks in the alignment
// between the WS handshake's org check and middleware.RequireOrgAccess (REST).
// A *DB* super-admin (user.SuperAdmin) whose access token is scoped to a
// different org with a non-super-admin role is 403'd by the REST middleware
// (only a *claims* super-admin may cross orgs there); the WS handshake used to
// let this exact shape through because it skipped the whole org check for any
// DB super-admin. Both paths now reject it identically.
func TestServe_DBSuperAdminForeignOrgClosesForbidden(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	// Org "test" (baked into fx.wsURL) must exist so the handshake reaches the
	// org-mismatch check rather than the "organization not found" branch.
	fx.seedOrgAndUser(t)

	// A DB super-admin whose *claims* are scoped to a different org ("other")
	// with role "admin" (not "superadmin"). Login always upgrades a
	// super-admin's claim role to superadmin, so mint the token via
	// GenerateTokensForOAuth, which honors the role passed verbatim — this is
	// the one credential shape the REST middleware rejects but the WS handshake
	// historically allowed.
	otherOrg := models.NewOrganization("other", "Other Org")
	r.NoError(fx.dbSvc.CreateOrganization(t.Context(), otherOrg))

	user := models.NewUser("dbadmin@example.com")
	pwd := "$plaintext$pw"
	user.PasswordHash = &pwd
	user.SuperAdmin = true
	r.NoError(fx.dbSvc.CreateUser(t.Context(), user))

	resp, err := fx.authService.GenerateTokensForOAuth(t.Context(), user, otherOrg, "admin", "", auth.Context{})
	r.NoError(err)

	conn := fx.dialPreAuth(t, resp.AccessToken)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, _, readErr := conn.Read(ctx)
	r.Error(readErr)
	r.Equal(realtimews.CloseForbidden, websocket.CloseStatus(readErr))
}

func TestServe_DisabledClosesImmediately4404(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{disabled: true})
	fx.seedOrgAndUser(t)
	token := fx.token(t)

	// Token auth succeeds at the HTTP level (validity does not depend on the
	// feature flag); the disabled feature is a post-upgrade 4404 close, so a
	// valid token is required to observe it.
	conn := fx.dialPreAuth(t, token)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	r.Error(err)
	r.Equal(realtimews.CloseDisabled, websocket.CloseStatus(err))
}

func TestServe_DefaultSilent_NoSubscriptionsReceivesNoUpdate(t *testing.T) {
	t.Parallel()

	fx := newWSFixture(t, wsFixtureOpts{})
	org := fx.seedOrgAndUser(t)
	check := fx.createCheck(t, org)
	token := fx.token(t)

	conn := fx.dialPreAuth(t, token)
	_ = readJSON[helloFrame](t, conn)
	fr := newFrameReader(t, conn)

	fx.pub.PublishImmediate(t.Context(), org.UID, check.UID, realtime.KindResults, realtime.KindChecks)

	fr.expectNoMessage(t, 500*time.Millisecond)
}

func TestServe_SubscribeCheckThenUpdateOnMatchingCheckOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	org := fx.seedOrgAndUser(t)
	watched := fx.createCheck(t, org)
	other := fx.createCheck(t, org)
	token := fx.token(t)

	conn := fx.dialPreAuth(t, token)
	_ = readJSON[helloFrame](t, conn)

	writeJSON(t, conn, map[string]string{"type": "subscribe", "entity": "check", "uid": watched.UID})
	sub := readJSON[subscribedFrame](t, conn)
	r.Equal("subscribed", sub.Type)
	r.Equal("check", sub.Entity)
	r.Equal(watched.UID, sub.UID)

	fr := newFrameReader(t, conn)

	// Activity on the other check must not arrive.
	fx.pub.PublishImmediate(t.Context(), org.UID, other.UID, realtime.KindResults)
	fr.expectNoMessage(t, 300*time.Millisecond)

	// Activity on the watched check arrives.
	fx.pub.PublishImmediate(t.Context(), org.UID, watched.UID, realtime.KindResults)
	var upd updateFrame
	fr.nextJSON(t, &upd)
	r.Equal("update", upd.Type)
	r.Equal("check", upd.Entity)
	r.Equal(watched.UID, upd.UID)
	r.Equal([]string{"results"}, upd.Kinds)
}

func TestServe_SubscribeForeignOrgCheckUidReturnsNotFoundErrorSocketStaysOpen(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	org := fx.seedOrgAndUser(t)
	token := fx.token(t)

	otherOrg := models.NewOrganization("otherorg", "Other Org")
	r.NoError(fx.dbSvc.CreateOrganization(t.Context(), otherOrg))
	foreignCheck := fx.createCheck(t, otherOrg)

	conn := fx.dialPreAuth(t, token)
	_ = readJSON[helloFrame](t, conn)

	writeJSON(t, conn, map[string]string{"type": "subscribe", "entity": "check", "uid": foreignCheck.UID})
	errFrame := readJSON[errorFrame](t, conn)
	r.Equal("error", errFrame.Type)
	r.Equal("NOT_FOUND", errFrame.Code)
	r.Equal("check", errFrame.Entity)
	r.Equal(foreignCheck.UID, errFrame.UID)

	// Socket stays open: a valid subscribe still works afterward.
	ownCheck := fx.createCheck(t, org)
	writeJSON(t, conn, map[string]string{"type": "subscribe", "entity": "check", "uid": ownCheck.UID})
	sub := readJSON[subscribedFrame](t, conn)
	r.Equal("subscribed", sub.Type)
}

func TestServe_DuplicateSubscribeIsIdempotentAck(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	org := fx.seedOrgAndUser(t)
	token := fx.token(t)

	conn := fx.dialPreAuth(t, token)
	_ = readJSON[helloFrame](t, conn)

	writeJSON(t, conn, map[string]string{"type": "subscribe", "entity": "checks"})
	first := readJSON[subscribedFrame](t, conn)
	r.Equal("subscribed", first.Type)

	writeJSON(t, conn, map[string]string{"type": "subscribe", "entity": "checks"})
	second := readJSON[subscribedFrame](t, conn)
	r.Equal("subscribed", second.Type, "duplicate subscribe must be an idempotent ack, not an error")

	_ = org
}

func TestServe_SubscriptionCapReturnsConcurrencyLimitedError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{maxSubscriptions: 1})
	org := fx.seedOrgAndUser(t)
	token := fx.token(t)

	conn := fx.dialPreAuth(t, token)
	_ = readJSON[helloFrame](t, conn)

	writeJSON(t, conn, map[string]string{"type": "subscribe", "entity": "checks"})
	first := readJSON[subscribedFrame](t, conn)
	r.Equal("subscribed", first.Type)

	writeJSON(t, conn, map[string]string{"type": "subscribe", "entity": "incidents"})
	errFrame := readJSON[errorFrame](t, conn)
	r.Equal("error", errFrame.Type)
	r.Equal("CONCURRENCY_LIMITED", errFrame.Code)

	_ = org
}

func TestServe_CollectionSubscriptionReceivesOrgLevelUpdate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	org := fx.seedOrgAndUser(t)
	token := fx.token(t)

	conn := fx.dialPreAuth(t, token)
	_ = readJSON[helloFrame](t, conn)

	writeJSON(t, conn, map[string]string{"type": "subscribe", "entity": "incidents"})
	_ = readJSON[subscribedFrame](t, conn)

	fx.pub.PublishImmediate(t.Context(), org.UID, "", realtime.KindIncidents, realtime.KindEvents)

	upd := readJSON[updateFrame](t, conn)
	r.Equal("update", upd.Type)
	r.Equal("incidents", upd.Entity)
	r.Equal([]string{"incidents"}, upd.Kinds)
}

func TestServe_TokenExpiryCloses4401(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)

	// authService config's AccessTokenExpiry is 1h in the fixture; instead of
	// waiting an hour, build a token directly with a short expiry via a second
	// auth service sharing the same JWT secret and db. A generous 5s window
	// keeps a slow dial from expiring the token before the pre-upgrade
	// validation runs (a real flake observed under `go test ./...`
	// parallelism); pre-upgrade validation happens during the handshake, so the
	// mint→validate window is just the dial itself.
	const tokenTTL = 5 * time.Second
	shortCfg := config.AuthConfig{
		JWTSecret:          "test-jwt-secret",
		AccessTokenExpiry:  tokenTTL,
		RefreshTokenExpiry: time.Hour,
	}
	// Shares the same JWT secret as the fixture; fullCfg must be non-nil so the
	// Service can sign/validate the short-lived token live via fullCfg.Auth.
	shortAuthService := auth.NewService(fx.dbSvc, shortCfg, &config.Config{Auth: shortCfg}, nil, nil)
	resp, err := shortAuthService.Login(t.Context(), "test", "member@example.com", "pw", auth.Context{})
	r.NoError(err)

	conn := fx.dialPreAuth(t, resp.AccessToken)
	_ = readJSON[helloFrame](t, conn)

	ctx, cancel := context.WithTimeout(t.Context(), tokenTTL+10*time.Second)
	defer cancel()
	_, _, readErr := conn.Read(ctx)
	r.Error(readErr)
	r.Equal(realtimews.CloseAuthFailed, websocket.CloseStatus(readErr))
}

func TestServe_StormCollapseWildcardReachesPerCheckSubscriber(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	org := fx.seedOrgAndUser(t)
	watched := fx.createCheck(t, org)
	token := fx.token(t)

	conn := fx.dialPreAuth(t, token)
	_ = readJSON[helloFrame](t, conn)

	writeJSON(t, conn, map[string]string{"type": "subscribe", "entity": "check", "uid": watched.UID})
	_ = readJSON[subscribedFrame](t, conn)

	// Drive well over CollapseCheckUids distinct check uids in one window so
	// the publisher collapses to the wildcard, then confirm the watched
	// check's subscriber still gets the update.
	for i := 0; i < realtime.CollapseCheckUids+5; i++ {
		fx.pub.Publish(t.Context(), org.UID, "storm-check", realtime.KindResults)
	}
	fx.pub.PublishImmediate(t.Context(), org.UID, watched.UID, realtime.KindResults)

	upd := readJSON[updateFrame](t, conn)
	r.Equal("update", upd.Type)
	r.Equal("check", upd.Entity)
	r.Equal(watched.UID, upd.UID)
}
