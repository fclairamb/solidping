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
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/realtimews"
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
// disable the feature, shrink the auth grace, lower the subscription cap).
type wsFixtureOpts struct {
	disabled         bool
	authGrace        time.Duration
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
	authService := auth.NewService(dbSvc, authCfg, nil, nil, nil)

	if opts.authGrace <= 0 {
		opts.authGrace = time.Second
	}
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
			AuthGrace:                     opts.authGrace,
			MaxSubscriptionsPerConnection: opts.maxSubscriptions,
		},
	}

	bus := notifier.NewLocalEventNotifier()
	hub := realtime.NewHubWithSubscriptionCap(
		bus, cfg.Realtime.MaxConnections, cfg.Realtime.MaxSubscriptionsPerConnection, nil)
	pub := realtime.NewPublisher(ctx, bus, cfg.Realtime.FlushInterval, nil)

	handler := realtimews.NewHandler(hub, authService, dbSvc, cfg)

	router := bunrouter.New()
	router.GET("/api/v1/orgs/:org/events/ws", func(w http.ResponseWriter, req bunrouter.Request) error {
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

// dial opens a raw WebSocket connection with no pre-auth header (the
// "browser" path — auth must be sent as the first message).
func (fx *wsFixture) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	r := require.New(t)

	conn, resp, err := websocket.Dial(t.Context(), fx.wsURL, nil)
	r.NoError(err)
	if resp != nil && resp.Body != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	return conn
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

func TestServe_BrowserAuthMessageThenHello(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)
	token := fx.token(t)

	conn := fx.dial(t)
	writeJSON(t, conn, map[string]string{"type": "auth", "token": token})

	hello := readJSON[helloFrame](t, conn)
	r.Equal("hello", hello.Type)
}

func TestServe_NoAuthWithinGraceWindowCloses4401(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{authGrace: 200 * time.Millisecond})
	fx.seedOrgAndUser(t)

	conn := fx.dial(t)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	r.Error(err)
	r.Equal(realtimews.CloseAuthFailed, websocket.CloseStatus(err))
}

func TestServe_SubscribeBeforeAuthCloses4401(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{authGrace: 2 * time.Second})
	fx.seedOrgAndUser(t)

	conn := fx.dial(t)
	writeJSON(t, conn, map[string]string{"type": "subscribe", "entity": "checks"})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	r.Error(err)
	r.Equal(realtimews.CloseAuthFailed, websocket.CloseStatus(err))
}

func TestServe_InvalidPreAuthTokenClosesImmediately(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{authGrace: 2 * time.Second})
	fx.seedOrgAndUser(t)

	conn := fx.dialPreAuth(t, "not-a-valid-token")

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	r.Error(err)
	r.Equal(realtimews.CloseAuthFailed, websocket.CloseStatus(err))
}

// TestServe_StaleCookieFallsThroughToAuthMessage covers the browser bug: the
// login/refresh endpoints set an access_token cookie for the unrelated
// OAuth-consent flow, and browsers attach it to every same-origin request
// automatically — including this upgrade. A stale/mismatched cookie (e.g.
// left over from an earlier login than the token the page currently holds)
// must not permanently kill the socket the way an explicit-but-invalid
// Authorization header does; the client's fresh in-band `auth` message gets
// a chance instead.
func TestServe_StaleCookieFallsThroughToAuthMessage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{authGrace: 2 * time.Second})
	fx.seedOrgAndUser(t)
	token := fx.token(t)

	conn := fx.dialWithCookie(t, "stale-invalid-cookie-token")
	writeJSON(t, conn, map[string]string{"type": "auth", "token": token})

	hello := readJSON[helloFrame](t, conn)
	r.Equal("hello", hello.Type)
}

// TestServe_ValidCookieAuthenticatesImmediately covers the CLI/websocat use
// case the cookie path exists for: a cookie that DOES validate authenticates
// at upgrade time, same as a valid Authorization header, without waiting for
// an auth message.
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

func TestServe_DisabledClosesImmediately4404(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fx := newWSFixture(t, wsFixtureOpts{disabled: true})
	fx.seedOrgAndUser(t)

	conn := fx.dial(t)

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
	// waiting an hour, build a token directly with a short expiry via a
	// second auth service sharing the same JWT secret and db. To avoid a
	// race against connection setup under load (a slow dial/hello round-trip
	// could otherwise let the token expire before the handler even validates
	// it — a real flake observed under `go test ./...` parallelism), the
	// token is minted with a generous 5s window and via the browser `auth`
	// message path (not pre-auth), so validation happens immediately after
	// the socket opens rather than after the full TCP+TLS dial.
	const tokenTTL = 5 * time.Second
	shortCfg := config.AuthConfig{
		JWTSecret:          "test-jwt-secret",
		AccessTokenExpiry:  tokenTTL,
		RefreshTokenExpiry: time.Hour,
	}
	shortAuthService := auth.NewService(fx.dbSvc, shortCfg, nil, nil, nil)
	resp, err := shortAuthService.Login(t.Context(), "test", "member@example.com", "pw", auth.Context{})
	r.NoError(err)

	conn := fx.dial(t)
	writeJSON(t, conn, map[string]string{"type": "auth", "token": resp.AccessToken})
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
