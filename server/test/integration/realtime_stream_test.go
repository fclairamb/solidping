package integration

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// enableRealtime turns the live hint stream on (the zero-value test config
// leaves it disabled) with a short flush window for fast tests.
func enableRealtime(cfg *config.Config) {
	cfg.Realtime = config.RealtimeConfig{
		Enabled:       true,
		FlushInterval: 50 * time.Millisecond,
		PingInterval:  25 * time.Second,
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

// openStream issues the stream GET and returns the raw response (caller
// closes the body).
func openStream(ctx context.Context, t *testing.T, ts *TestServer, org, token string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.HTTPServer.URL+"/api/v1/orgs/"+org+"/events/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := ts.HTTPServer.Client().Do(req)
	require.NoError(t, err)

	return resp
}

// TestRealtimeStream_EndToEndHeartbeatHint exercises the full slice: an
// authenticated org member holds the SSE stream open while a heartbeat
// result is ingested; the stream delivers `hello` then a hint carrying (at
// least) `results`.
func TestRealtimeStream_EndToEndHeartbeatHint(t *testing.T) {
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

	resp := openStream(streamCtx, t, ts, TestOrgSlug, token)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("text/event-stream", resp.Header.Get("Content-Type"))

	scanner := bufio.NewScanner(resp.Body)

	// hello arrives first.
	event, data := readSSEEvent(t, scanner)
	r.Equal("hello", event)
	r.Contains(data, `"protocol":1`)

	// Ingest a heartbeat result through the public endpoint.
	hbReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.HTTPServer.URL+"/api/v1/heartbeat/"+TestOrgSlug+"/"+check.UID+"?token=hb-secret&status=up", nil)
	r.NoError(err)
	hbResp, err := ts.HTTPServer.Client().Do(hbReq)
	r.NoError(err)
	_ = hbResp.Body.Close()
	r.Equal(http.StatusOK, hbResp.StatusCode)

	// The stream delivers a hint (possibly several, coalesced) that includes
	// the results kind.
	for {
		event, data = readSSEEvent(t, scanner)
		if event == "hint" && strings.Contains(data, `"results"`) {
			break
		}
	}
}

// TestRealtimeStream_ForbiddenForOtherOrgToken pins the membership gate: an
// access token scoped to org A gets 403 (per frontend-errors conventions) on
// org B's stream, before any SSE handshake.
func TestRealtimeStream_ForbiddenForOtherOrgToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	ts := NewTestServerWithConfig(t, enableRealtime)
	token := streamLoginToken(ctx, t, ts) // scoped to TestOrgSlug

	resp := openStream(ctx, t, ts, TestOrg2Slug, token)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusForbidden, resp.StatusCode)
}

// TestRealtimeStream_UnauthenticatedRejected pins the auth gate.
func TestRealtimeStream_UnauthenticatedRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	ts := NewTestServerWithConfig(t, enableRealtime)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.HTTPServer.URL+"/api/v1/orgs/"+TestOrgSlug+"/events/stream", nil)
	r.NoError(err)
	resp, err := ts.HTTPServer.Client().Do(req)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusUnauthorized, resp.StatusCode)
}

// TestRealtimeStream_DisabledReturns404 pins the SP_REALTIME_ENABLED=false
// convention: the route is not registered, so the endpoint 404s and the
// dashboard silently keeps polling.
func TestRealtimeStream_DisabledReturns404(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	ts := NewTestServer(t) // zero-value config: realtime disabled
	token := streamLoginToken(ctx, t, ts)

	resp := openStream(ctx, t, ts, TestOrgSlug, token)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusNotFound, resp.StatusCode)
}

// readSSEEvent scans until one complete SSE event frame has been read.
func readSSEEvent(t *testing.T, scanner *bufio.Scanner) (string, string) {
	t.Helper()

	var event string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			return event, strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatalf("stream ended before an event arrived: %v", scanner.Err())

	return "", ""
}
