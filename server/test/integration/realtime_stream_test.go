package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/realtimews"
)

// enableRealtime turns the live hint WebSocket on with a short flush window
// and auth grace for fast tests, plus a small subscription cap for the
// dedicated cap test.
func enableRealtime(cfg *config.Config) {
	cfg.Realtime = config.RealtimeConfig{
		Enabled:                       true,
		FlushInterval:                 50 * time.Millisecond,
		PingInterval:                  25 * time.Second,
		AuthGrace:                     2 * time.Second,
		MaxSubscriptionsPerConnection: 512,
	}
}

// streamLoginToken logs the shared test user into the primary test org and
// returns the org-scoped access token.
func streamLoginToken(ctx context.Context, t *testing.T, ts *TestServer) string {
	t.Helper()

	resp, err := ts.Client.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	require.NoError(t, err)
	require.NotNil(t, resp.AccessToken)

	return *resp.AccessToken
}

// wsURL converts the test server's http(s) base URL into a ws(s) URL for the
// given org's realtime endpoint.
func wsURL(ts *TestServer, org string) string {
	return "ws" + strings.TrimPrefix(ts.HTTPServer.URL, "http") + "/api/v1/orgs/" + org + "/events/ws"
}

// dialRealtimeWithToken opens a pre-authenticated connection (Authorization
// header set at upgrade time — the CLI/test/websocat path).
func dialRealtimeWithToken(ctx context.Context, t *testing.T, ts *TestServer, org, token string) *websocket.Conn {
	t.Helper()

	conn, _, err := websocket.Dial(ctx, wsURL(ts, org), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	return conn
}

type wsHello struct {
	Type     string `json:"type"`
	Protocol int    `json:"protocol"`
}

type wsSubscribed struct {
	Type   string `json:"type"`
	Entity string `json:"entity"`
	UID    string `json:"uid,omitempty"`
}

type wsUpdate struct {
	Type   string   `json:"type"`
	Entity string   `json:"entity"`
	UID    string   `json:"uid,omitempty"`
	Kinds  []string `json:"kinds,omitempty"`
}

func readWSJSON[T any](ctx context.Context, t *testing.T, conn *websocket.Conn) T {
	t.Helper()

	_, data, err := conn.Read(ctx)
	require.NoError(t, err)

	var v T
	require.NoError(t, json.Unmarshal(data, &v))

	return v
}

func writeWSJSON(ctx context.Context, t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, data))
}

// TestRealtimeWS_EndToEndHeartbeatHint exercises the full slice through the
// real router/middleware chain: an authenticated org member subscribes to
// the org's `checks` collection while a heartbeat result is ingested; the
// socket delivers `hello`, `subscribed`, then an `update` carrying `results`.
func TestRealtimeWS_EndToEndHeartbeatHint(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	ts := NewTestServerWithConfig(t, enableRealtime)
	token := streamLoginToken(ctx, t, ts)

	// A heartbeat check the test can ping without any worker involved.
	dbService := ts.Server.DBService()
	check := models.NewCheck("10000000-0000-0000-0000-000000000001", "hb-live", "heartbeat")
	check.Config = models.JSONMap{"token": "hb-secret"}
	r.NoError(dbService.CreateCheck(ctx, check))

	streamCtx, cancelStream := context.WithTimeout(ctx, 15*time.Second)
	defer cancelStream()

	conn := dialRealtimeWithToken(streamCtx, t, ts, TestOrgSlug, token)

	hello := readWSJSON[wsHello](streamCtx, t, conn)
	r.Equal("hello", hello.Type)
	r.Equal(realtimews.ProtocolVersion, hello.Protocol)

	writeWSJSON(streamCtx, t, conn, map[string]string{"type": "subscribe", "entity": "checks"})
	sub := readWSJSON[wsSubscribed](streamCtx, t, conn)
	r.Equal("subscribed", sub.Type)
	r.Equal("checks", sub.Entity)

	// Ingest a heartbeat result through the public endpoint.
	hbReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.HTTPServer.URL+"/api/v1/heartbeat/"+TestOrgSlug+"/"+check.UID+"?token=hb-secret&status=up", nil)
	r.NoError(err)
	hbResp, err := ts.HTTPServer.Client().Do(hbReq)
	r.NoError(err)
	_ = hbResp.Body.Close()
	r.Equal(http.StatusOK, hbResp.StatusCode)

	// The socket delivers an update (possibly several, coalesced) that
	// includes the results kind on the checks collection scope.
	for {
		upd := readWSJSON[wsUpdate](streamCtx, t, conn)
		if upd.Type == "update" && upd.Entity == "checks" {
			found := false
			for _, k := range upd.Kinds {
				if k == "results" {
					found = true
				}
			}
			if found {
				break
			}
		}
	}
}

// TestRealtimeWS_ForbiddenForOtherOrgToken pins the membership gate: an
// access token scoped to org A gets a 4403 close on org B's socket, before
// any subscription is possible.
func TestRealtimeWS_ForbiddenForOtherOrgToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	ts := NewTestServerWithConfig(t, enableRealtime)
	token := streamLoginToken(ctx, t, ts) // scoped to TestOrgSlug

	conn := dialRealtimeWithToken(ctx, t, ts, TestOrg2Slug, token)

	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(readCtx)
	r.Error(err)
	r.Equal(realtimews.CloseForbidden, websocket.CloseStatus(err))
}

// TestRealtimeWS_UnauthenticatedTimesOutWithAuthFailed pins the auth gate:
// no Authorization header and no `auth` message within the grace window
// closes 4401.
func TestRealtimeWS_UnauthenticatedTimesOutWithAuthFailed(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	ts := NewTestServerWithConfig(t, func(cfg *config.Config) {
		enableRealtime(cfg)
		cfg.Realtime.AuthGrace = 300 * time.Millisecond
	})

	conn, _, err := websocket.Dial(ctx, wsURL(ts, TestOrgSlug), nil)
	r.NoError(err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, readErr := conn.Read(readCtx)
	r.Error(readErr)
	r.Equal(realtimews.CloseAuthFailed, websocket.CloseStatus(readErr))
}

// TestRealtimeWS_DisabledClosesImmediately4404 pins the SP_REALTIME_ENABLED=false
// convention: the route is still registered (browsers can't see a rejected
// upgrade), but the socket is accepted and immediately closed 4404 so the
// dashboard silently keeps polling.
func TestRealtimeWS_DisabledClosesImmediately4404(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	ts := NewTestServer(t) // zero-value config: realtime disabled
	token := streamLoginToken(ctx, t, ts)

	conn := dialRealtimeWithToken(ctx, t, ts, TestOrgSlug, token)

	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(readCtx)
	r.Error(err)
	r.Equal(realtimews.CloseDisabled, websocket.CloseStatus(err))
}
