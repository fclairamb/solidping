package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// newToolsTestServer boots a real server (NewServer + SetupRoutes) with the
// public tools endpoint enabled unless mutate turns it off.
func newToolsTestServer(t *testing.T, mutate func(cfg *config.Config)) (*Server, *httptest.Server) {
	t.Helper()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "tools-route-secret"
	cfg.Server.BaseURL = "https://solidping.acme.com"
	cfg.Tools.Enabled = true

	if mutate != nil {
		mutate(cfg)
	}

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	return server, ts
}

type toolsErrorBody struct {
	Code  string `json:"code"`
	Title string `json:"title"`
}

// toolRunResponse is what a test needs from one response; the body is read
// and closed by postToolRun.
type toolRunResponse struct {
	StatusCode int
	Header     http.Header
}

func postToolRun(t *testing.T, client *http.Client, baseURL, body string) (toolRunResponse, toolsErrorBody) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		baseURL+"/api/v1/tools/run", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://www.solidping.io")

	resp, err := client.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	var decoded toolsErrorBody
	_ = json.NewDecoder(resp.Body).Decode(&decoded)

	return toolRunResponse{StatusCode: resp.StatusCode, Header: resp.Header}, decoded
}

func TestToolsRunIs404WhileDisabled(t *testing.T) {
	t.Parallel()

	_, ts := newToolsTestServer(t, func(cfg *config.Config) { cfg.Tools.Enabled = false })

	resp, body := postToolRun(t, ts.Client(), ts.URL, `{"type":"dns","target":"localhost"}`)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "NOT_FOUND", body.Code)
}

func TestToolsRunAnswersCORSForAnyOrigin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.True(isPublicCORSPath("/api/v1/tools/run"))

	_, ts := newToolsTestServer(t, nil)

	preflight, err := http.NewRequestWithContext(context.Background(), http.MethodOptions,
		ts.URL+"/api/v1/tools/run", nil)
	r.NoError(err)
	preflight.Header.Set("Origin", "https://www.solidping.io")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)

	resp, err := ts.Client().Do(preflight)
	r.NoError(err)
	_ = resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("*", resp.Header.Get("Access-Control-Allow-Origin"))
	r.Empty(resp.Header.Get("Access-Control-Allow-Credentials"))

	run, _ := postToolRun(t, ts.Client(), ts.URL, `{"type":"dns","target":"localhost"}`)
	r.Equal(http.StatusOK, run.StatusCode)
	r.Equal("*", run.Header.Get("Access-Control-Allow-Origin"))
}

// The 11th run inside a minute from one IP is refused, with the standard
// error shape. Each run targets loopback, so it is refused by the guard
// without any network traffic, and still counts.
func TestToolsRunIsRateLimitedPerIP(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, ts := newToolsTestServer(t, nil)
	client := ts.Client()

	for i := range toolsRunsPerMinute {
		resp, body := postToolRun(t, client, ts.URL, `{"type":"tcp","target":"127.0.0.1:22"}`)
		r.Equal(http.StatusForbidden, resp.StatusCode, "run %d is admitted (and refused by the guard)", i+1)
		r.Equal("TARGET_NOT_ALLOWED", body.Code)
	}

	resp, body := postToolRun(t, client, ts.URL, `{"type":"tcp","target":"127.0.0.1:22"}`)
	r.Equal(http.StatusTooManyRequests, resp.StatusCode)
	r.Equal("RATE_LIMITED", body.Code)
	r.NotEmpty(resp.Header.Get("Retry-After"))
}

// SP_EGRESS_ALLOW_PRIVATE=true opens the LAN to this instance's check
// workers, never to anonymous visitors: a live loopback listener is still
// refused.
func TestToolsRunRefusesLoopbackEvenWhenPrivateTargetsAreAllowed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)
	t.Cleanup(func() { _ = listener.Close() })

	allow := true
	server, ts := newToolsTestServer(t, func(cfg *config.Config) { cfg.Egress.AllowPrivateTargets = &allow })
	r.True(server.config.EgressAllowsPrivateTargets(), "positive control: the instance policy does allow private targets")

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	r.True(ok)

	port := strconv.Itoa(tcpAddr.Port)

	for _, body := range []string{
		`{"type":"tcp","target":"127.0.0.1:` + port + `"}`,
		`{"type":"tcp","target":"localhost:` + port + `"}`,
		`{"type":"http","target":"http://127.0.0.1:` + port + `/"}`,
		`{"type":"ssl","target":"127.0.0.1:` + port + `"}`,
	} {
		resp, decoded := postToolRun(t, ts.Client(), ts.URL, body)
		r.Equal(http.StatusForbidden, resp.StatusCode, body)
		r.Equal("TARGET_NOT_ALLOWED", decoded.Code, body)
	}
}

func TestToolsRunRefusesTheInstanceOwnHost(t *testing.T) {
	t.Parallel()

	_, ts := newToolsTestServer(t, nil)

	resp, body := postToolRun(t, ts.Client(), ts.URL, `{"type":"http","target":"https://solidping.acme.com/api/v1/orgs"}`)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, "TARGET_NOT_ALLOWED", body.Code)
}

func TestToolsRunWritesNoRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	server, ts := newToolsTestServer(t, nil)
	ctx := context.Background()

	count := func() map[string]int {
		counts := map[string]int{}

		for name, model := range map[string]any{
			"checks": (*models.Check)(nil), "results": (*models.Result)(nil), "incidents": (*models.Incident)(nil),
		} {
			n, err := server.dbService.DB().NewSelect().Model(model).Count(ctx)
			r.NoError(err)

			counts[name] = n
		}

		return counts
	}

	before := count()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/v1/tools/run",
		strings.NewReader(`{"type":"dns","target":"localhost","options":{"record":"A"}}`))
	r.NoError(err)

	resp, err := ts.Client().Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)

	var result struct {
		Type    string         `json:"type"`
		Target  string         `json:"target"`
		Status  string         `json:"status"`
		Cached  bool           `json:"cached"`
		Details map[string]any `json:"details"`
		Monitor struct {
			CheckType string `json:"checkType"`
			Query     string `json:"query"`
		} `json:"monitor"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&result))
	r.Equal("dns", result.Type)
	r.Equal("localhost", result.Target)
	r.Equal("up", result.Status)
	r.Equal("dns", result.Monitor.CheckType)

	r.Equal(before, count(), "a tool run persists nothing")
}
