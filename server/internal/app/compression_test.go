package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/statuspagecache"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// --- spec 2026-09-22-04: response compression, exercised end to end -------
//
// These tests go through the REAL Server.Handler() (NewServer + SetupRoutes),
// the same path every app-level test in this package uses, so the compression
// wrapper is exercised at its real placement: outside the httpx router and
// its middleware chain.

const (
	compressionTestOrg      = "compression-e2e"
	compressionTestPageSlug = "public"
)

// compressionEnv is a fully wired server on an httptest socket, with one
// seeded org that owns a public status page carrying enough resources for the
// page-view JSON to comfortably clear gzhttp's 1 KiB MinSize.
type compressionEnv struct {
	t        *testing.T
	server   *Server
	ts       *httptest.Server
	client   *http.Client
	pageSlug string
}

func newCompressionEnv(t *testing.T, enabled bool) *compressionEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "compression-e2e-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour
	cfg.Server.Compression = enabled
	cfg.Realtime.Enabled = true

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	org := models.NewOrganization(compressionTestOrg, "Compression E2E")
	r.NoError(server.dbService.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Public", compressionTestPageSlug)
	page.IsDefault = true
	r.NoError(server.dbService.CreateStatusPage(ctx, page))

	section := models.NewStatusPageSection(page.UID, "Services", "services", 0)
	r.NoError(server.dbService.CreateStatusPageSection(ctx, section))

	// Five checks with 90 days of daily-availability history each puts the
	// page view comfortably north of MinSize(1024) — the acceptance bar is
	// "well under 10% of uncompressed", which needs a real body, not a
	// handful of bytes.
	const seededChecks = 5
	for i := range seededChecks {
		check := models.NewCheck(org.UID, fmt.Sprintf("check-%d", i), "http")
		name := fmt.Sprintf("Check number %d with a fairly long descriptive name", i)
		check.Name = &name
		r.NoError(server.dbService.CreateCheck(ctx, check))

		res := models.NewStatusPageResource(section.UID, check.UID, i)
		r.NoError(server.dbService.CreateStatusPageResource(ctx, res))
	}

	return &compressionEnv{
		t: t, server: server, ts: ts,
		client:   &http.Client{},
		pageSlug: page.Slug,
	}
}

// rawGet issues a GET with the given headers and returns the response with
// the body UNREAD, so the caller can inspect headers before deciding how (or
// whether) to decode the body. Callers must close the body.
func (e *compressionEnv) rawGet(path string, headers map[string]string) *http.Response {
	e.t.Helper()
	r := require.New(e.t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.ts.URL+path, nil)
	r.NoError(err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	r.NoError(err)

	return resp
}

func (e *compressionEnv) statusPagePath() string {
	return "/api/v1/status-pages/" + compressionTestOrg + "/" + e.pageSlug
}

// TestCompressionEncodesLargeStatusPageResponse is the spec's core
// acceptance: a large public JSON body is gzip-encoded when the client asks
// for it, well under 10% of its uncompressed size, extends Vary rather than
// replacing it, and decodes back to exactly the same bytes the uncompressed
// response carries.
func TestCompressionEncodesLargeStatusPageResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newCompressionEnv(t, true)

	encoded := env.rawGet(env.statusPagePath(), map[string]string{"Accept-Encoding": "gzip"})
	defer func() { _ = encoded.Body.Close() }()

	r.Equal(http.StatusOK, encoded.StatusCode)
	r.Equal("gzip", encoded.Header.Get("Content-Encoding"))

	vary := strings.Join(encoded.Header.Values("Vary"), ", ")
	r.Contains(vary, statuspagecache.VaryPublic,
		"the handler's own pinned Vary value must survive the wrapper")
	r.Contains(vary, "Accept-Encoding",
		"gzhttp must extend Vary with Accept-Encoding rather than replace it")

	rawEncoded, err := io.ReadAll(encoded.Body)
	r.NoError(err)

	// gzhttp deletes the ORIGINAL (uncompressed) Content-Length it found on
	// the handler's response. Go's net/http may then compute a fresh one on
	// the truly-compressed bytes if the whole response fits in its internal
	// buffer before headers are flushed — either way, a Content-Length
	// header, if present at all, must describe the compressed body actually
	// on the wire, never the uncompressed one.
	if cl := encoded.Header.Get("Content-Length"); cl != "" {
		r.Equal(strconv.Itoa(len(rawEncoded)), cl,
			"a present Content-Length must be the compressed size, not the original")
	}

	gzReader, err := gzip.NewReader(bytes.NewReader(rawEncoded))
	r.NoError(err)
	decoded, err := io.ReadAll(gzReader)
	r.NoError(err)
	r.NoError(gzReader.Close())

	plain := env.rawGet(env.statusPagePath(), nil)
	defer func() { _ = plain.Body.Close() }()
	r.Equal(http.StatusOK, plain.StatusCode)
	r.Empty(plain.Header.Get("Content-Encoding"),
		"a request with no Accept-Encoding must not be compressed")

	plainBody, err := io.ReadAll(plain.Body)
	r.NoError(err)

	r.Equal(plainBody, decoded, "decoded body must equal the uncompressed response byte-for-byte")

	// The acceptance bar from the spec: well under 10% of the uncompressed
	// size for a body this size and this repetitive (JSON with long runs of
	// similar per-check structure compresses very well even at BestSpeed).
	r.Greater(len(plainBody), 1024, "fixture must actually exceed MinSize for this test to mean anything")
	r.Less(float64(len(rawEncoded)), 0.10*float64(len(plainBody)),
		"compressed transfer must be well under 10% of the uncompressed size")
}

// TestCompressionLeavesSmallResponseUnencoded confirms the MinSize floor: a
// body under 1 KiB is left alone even when the client asks for gzip and the
// content type is on the allowlist.
func TestCompressionLeavesSmallResponseUnencoded(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newCompressionEnv(t, true)

	resp := env.rawGet("/api/mgmt/health", map[string]string{"Accept-Encoding": "gzip"})
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Less(len(body), 1024, "fixture assumption: /api/mgmt/health is a small JSON body")
	r.Empty(resp.Header.Get("Content-Encoding"), "a sub-MinSize body must not be gzip-encoded")
}

// TestCompressionEncodesEmbeddedStatus0JS confirms the embedded SPA bundle —
// served with an explicit Content-Length by writeEmbeddedFile — is still
// encoded correctly, and that gzhttp drops the explicit Content-Length it no
// longer describes the true wire size for.
func TestCompressionEncodesEmbeddedStatus0JS(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newCompressionEnv(t, true)

	assetPath := findStatus0JSAsset(t)

	resp := env.rawGet(config.StatusBasePath+"/"+assetPath, map[string]string{"Accept-Encoding": "gzip"})
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("gzip", resp.Header.Get("Content-Encoding"))
	r.Empty(resp.Header.Get("Content-Length"))

	gzReader, err := gzip.NewReader(resp.Body)
	r.NoError(err)
	decoded, err := io.ReadAll(gzReader)
	r.NoError(err)
	r.NotEmpty(decoded)
}

// findStatus0JSAsset walks the embedded status0res/assets directory (the real
// production bundle, not a test double) for the first .js file and returns
// its path relative to status0res, e.g. "assets/index-abc123.js".
func findStatus0JSAsset(t *testing.T) string {
	t.Helper()

	var found string

	err := fs.WalkDir(status0Files, "status0res", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if found != "" || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".js") {
			found = strings.TrimPrefix(p, "status0res/")
		}

		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, found, "expected at least one embedded status0 .js asset")

	return found
}

// TestCompressionLeavesMCPEventStreamProbeUnencoded confirms text/event-stream
// stays off the allowlist: an MCP client probing GET with
// Accept: text/event-stream still gets its documented (small, JSON) 405
// answer, never gzip-wrapped. The MCP endpoint does not implement a real
// server-initiated SSE stream (HandleGet always answers a small JSON body or
// a redirect — see internal/mcp/handler.go's acceptsEventStream), so this is
// the closest thing to "exercise the SSE negotiation path" the endpoint
// offers; the allowlist test below is what actually protects a future real
// stream from being buffered.
func TestCompressionLeavesMCPEventStreamProbeUnencoded(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newCompressionEnv(t, true)

	resp := env.rawGet("/api/v1/mcp", map[string]string{
		"Accept":          "text/event-stream",
		"Accept-Encoding": "gzip",
	})
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusMethodNotAllowed, resp.StatusCode)
	r.Empty(resp.Header.Get("Content-Encoding"))

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	var errResp map[string]any
	r.NoError(json.Unmarshal(body, &errResp))
}

// TestCompressibleContentTypesExcludeEventStream is the allowlist assertion
// the spec calls for: text/event-stream (and every binary type) must never be
// on the list gzhttp is configured with, or a real SSE stream would be
// buffered and the protocol would break.
func TestCompressibleContentTypesExcludeEventStream(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.NotContains(compressibleContentTypes, "text/event-stream")
	r.NotContains(compressibleContentTypes, "application/octet-stream")
	r.Contains(compressibleContentTypes, "application/json")
	r.Contains(compressibleContentTypes, "text/html")
}

// TestCompressionRealtimeWebSocketStillUpgrades confirms the realtime hint
// WebSocket route (registered outside RequireAuth, authenticated in-band)
// still completes its upgrade through the wrapped handler: gzhttp's Hijacker
// passthrough must let the 101 Switching Protocols response through
// untouched, never attempt to encode it.
func TestCompressionRealtimeWebSocketStillUpgrades(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newCompressionEnv(t, true)

	email := "realtime-ws@example.com"
	password := "realtime-ws-password-1234"

	// Seed a member with a password directly through the DB service (mirrors
	// the pattern in mcp_endpoint_test.go / org_rename_redirect_test.go).
	org, err := env.server.dbService.GetOrganizationBySlug(context.Background(), compressionTestOrg)
	r.NoError(err)

	hash, err := passwords.Hash(password)
	r.NoError(err)
	now := time.Now()
	user := models.NewUser(email)
	user.PasswordHash = &hash
	user.EmailVerifiedAt = &now
	r.NoError(env.server.dbService.CreateUser(context.Background(), user))
	r.NoError(env.server.dbService.CreateOrganizationMember(
		context.Background(), models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	loginBody := fmt.Sprintf(`{"org":%q,"email":%q,"password":%q}`, compressionTestOrg, email, password)
	loginReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.ts.URL+"/api/v1/auth/login", strings.NewReader(loginBody))
	r.NoError(err)
	loginReq.Header.Set("Content-Type", "application/json")

	loginResp, err := env.client.Do(loginReq)
	r.NoError(err)
	defer func() { _ = loginResp.Body.Close() }()
	loginRespBody, err := io.ReadAll(loginResp.Body)
	r.NoError(err)
	r.Equal(http.StatusOK, loginResp.StatusCode, "login failed: %s", loginRespBody)

	var loginJSON struct {
		AccessToken string `json:"accessToken"`
	}
	r.NoError(json.Unmarshal(loginRespBody, &loginJSON))
	r.NotEmpty(loginJSON.AccessToken)

	wsURL := "ws" + strings.TrimPrefix(env.ts.URL, "http") +
		"/api/v1/orgs/" + compressionTestOrg + "/events/ws"

	conn, resp, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + loginJSON.AccessToken}},
	})
	r.NoError(err)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	r.NotNil(conn)
	_ = conn.CloseNow()
}

// TestCompressionDisabledNeverEncodes is the kill switch: with
// server.compression=false nothing is ever gzip-encoded, however large the
// body or however plainly the client asks for it.
func TestCompressionDisabledNeverEncodes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newCompressionEnv(t, false)

	resp := env.rawGet(env.statusPagePath(), map[string]string{"Accept-Encoding": "gzip"})
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Empty(resp.Header.Get("Content-Encoding"))

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Greater(len(body), 1024, "fixture must still be large enough that MinSize is not why it's uncompressed")
	r.False(bytes.HasPrefix(body, []byte{0x1f, 0x8b}), "body must not be gzip-magic-byte prefixed")
}
