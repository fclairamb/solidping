package mcp

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/httpx"
)

// denyAll stands in for RequireMCPAuth: it refuses everything, so any request
// that reaches the "authenticated" branch is visibly refused in these tests.
func denyAll(_ httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) error {
		writer.WriteHeader(http.StatusUnauthorized)

		return nil
	}
}

// gateProbe wires AllowAnonymousHandshake(denyAll) around a terminal handler
// that records the body it received, and returns the recorder plus what the
// terminal handler saw (empty when it was never reached).
func gateProbe(t *testing.T, body string, header map[string]string) (*httptest.ResponseRecorder, string, bool) {
	t.Helper()

	var (
		seen    string
		reached bool
	)

	terminal := func(writer http.ResponseWriter, req *http.Request) error {
		reached = true
		raw, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		seen = string(raw)
		writer.WriteHeader(http.StatusOK)

		return nil
	}

	handler := AllowAnonymousHandshake(denyAll)(terminal)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/v1/mcp", strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	require.NoError(t, handler(rec, req))

	return rec, seen, reached
}

func TestAllowAnonymousHandshake_LetsHandshakeThrough(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	rec, seen, reached := gateProbe(t, body, nil)

	r.True(reached)
	r.Equal(http.StatusOK, rec.Code)
	// The gate read the body to make its decision and put every byte back.
	r.Equal(body, seen)
}

func TestAllowAnonymousHandshake_RestoresBodyOnTheAuthenticatedPath(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// A body larger than one read, so a naive restore that only replays the
	// first chunk would be caught.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_checks",` +
		`"arguments":{"note":"` + strings.Repeat("x", 8*1024) + `"}}}`

	var seen string

	terminal := func(writer http.ResponseWriter, req *http.Request) error {
		raw, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		seen = string(raw)
		writer.WriteHeader(http.StatusOK)

		return nil
	}

	// passThrough stands in for a SUCCESSFUL RequireMCPAuth: the request
	// continues to the handler, which must still see the whole body.
	passThrough := func(next httpx.HandlerFunc) httpx.HandlerFunc { return next }

	handler := AllowAnonymousHandshake(passThrough)(terminal)
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/v1/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token-abc")
	rec := httptest.NewRecorder()

	r.NoError(handler(rec, req))
	r.Equal(http.StatusOK, rec.Code)
	r.Equal(body, seen)
}

func TestAllowAnonymousHandshake_OversizedBodyIsRestoredIntact(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Beyond the probe cap: the gate stops reading, refuses the anonymous
	// path, and the authenticated path still sees the complete body.
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"pad":"` +
		strings.Repeat("y", maxHandshakeProbeBytes*2) + `"}}`

	var seen string

	terminal := func(writer http.ResponseWriter, req *http.Request) error {
		raw, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		seen = string(raw)

		return nil
	}

	passThrough := func(next httpx.HandlerFunc) httpx.HandlerFunc { return next }
	handler := AllowAnonymousHandshake(passThrough)(terminal)
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/v1/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token-abc")
	rec := httptest.NewRecorder()

	r.NoError(handler(rec, req))
	r.Equal(body, seen)

	// And with no credentials it is NOT served anonymously.
	_, _, reached := gateProbe(t, body, nil)
	r.False(reached)
}

func TestAllowAnonymousHandshake_RefusesNonHandshake(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"tools/list":           `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		"batch with handshake": `[{"jsonrpc":"2.0","id":1,"method":"initialize"}]`,
		"batch smuggling": `[{"jsonrpc":"2.0","id":1,"method":"initialize"},` +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`,
		"concatenated": `{"jsonrpc":"2.0","id":1,"method":"initialize"}{"jsonrpc":"2.0","id":2,` +
			`"method":"tools/list"}`,
		"prefix extension": `{"jsonrpc":"2.0","id":1,"method":"initialize_and_dump"}`,
		"uppercase":        `{"jsonrpc":"2.0","id":1,"method":"INITIALIZE"}`,
		"padded":           `{"jsonrpc":"2.0","id":1,"method":" initialize "}`,
		"duplicate key":    `{"jsonrpc":"2.0","id":1,"method":"initialize","method":"tools/list"}`,
		"no method":        `{"jsonrpc":"2.0","id":1}`,
		"not json":         `not json at all`,
		"empty body":       ``,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			rec, _, reached := gateProbe(t, body, nil)
			r.False(reached, "gate let %q through", body)
			r.Equal(http.StatusUnauthorized, rec.Code)
		})
	}
}

func TestAllowAnonymousHandshake_CredentialsAlwaysGoThroughAuth(t *testing.T) {
	t.Parallel()

	cases := map[string]map[string]string{
		"bearer":             {"Authorization": "Bearer stale-token"},
		"malformed scheme":   {"Authorization": "Basic Zm9vOmJhcg=="},
		"empty-ish but sent": {"Authorization": "Bearer"},
	}

	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			rec, _, reached := gateProbe(t, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, header)
			r.False(reached)
			r.Equal(http.StatusUnauthorized, rec.Code)
		})
	}
}

func TestAllowAnonymousHandshake_NonPostGoesThroughAuth(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var reached bool

	handler := AllowAnonymousHandshake(denyAll)(func(writer http.ResponseWriter, _ *http.Request) error {
		reached = true

		return nil
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/mcp",
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	rec := httptest.NewRecorder()

	r.NoError(handler(rec, req))
	r.False(reached)
	r.Equal(http.StatusUnauthorized, rec.Code)
}

func TestIsAnonymousMethod(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(isAnonymousMethod(methodInitialize))
	r.True(isAnonymousMethod(methodInitialized))

	for _, method := range []string{
		methodToolsList, methodToolsCall, methodResourcesList, methodResourcesRead,
		methodPromptsList, methodPromptsGet, methodPing,
		"initialize_and_dump", "Initialize", " initialize", "", "initialize/",
	} {
		r.False(isAnonymousMethod(method), "method %q must not be anonymous", method)
	}
}

// --- Handler-level second layer (independent of the routing gate) ---

func TestHandle_AnonymousInitializeMintsNoSession(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	rec, req := makeRequest(t, http.MethodPost,
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`, nil)
	r.NoError(handler.Handle(rec, req))

	r.Equal(http.StatusOK, rec.Code)
	r.Empty(rec.Header().Get("Mcp-Session-Id"))

	resp := decodeResponse(t, rec)
	r.Nil(resp.Error)

	count := 0
	handler.sessions.Range(func(_, _ any) bool {
		count++

		return true
	})
	r.Zero(count, "an anonymous handshake must not store a session")
}

func TestHandle_AnonymousInitializedNotification(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	rec, req := makeRequest(t, http.MethodPost,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	r.NoError(handler.Handle(rec, req))
	r.Equal(http.StatusAccepted, rec.Code)
}

// The handler refuses non-handshake methods on its own, so a future routing
// mistake cannot turn into data exposure.
func TestHandle_AnonymousNonHandshakeIs401(t *testing.T) {
	t.Parallel()

	for _, method := range []string{
		methodToolsList, methodToolsCall, methodResourcesList,
		methodPromptsList, methodPing, "initialize_and_dump",
	} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			handler := newTestHandler()
			rec, req := makeRequest(t, http.MethodPost,
				`{"jsonrpc":"2.0","id":1,"method":"`+method+`"}`, nil)
			r.NoError(handler.Handle(rec, req))

			r.Equal(http.StatusUnauthorized, rec.Code)
			r.NotContains(rec.Body.String(), "list_checks")

			resp := decodeResponse(t, rec)
			r.NotNil(resp.Error)
			r.Equal(CodeInvalidRequest, resp.Error.Code)
		})
	}
}

func TestHandle_AuthenticatedInitializeStillMintsASession(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	rec, req := makeRequest(t, http.MethodPost,
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`, defaultClaims())
	r.NoError(handler.Handle(rec, req))

	r.Equal(http.StatusOK, rec.Code)
	r.NotEmpty(rec.Header().Get("Mcp-Session-Id"))
}
