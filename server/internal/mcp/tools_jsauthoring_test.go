package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/aichecks"
)

func jsAuthoringHandler(t *testing.T) (*Handler, string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"projects":[1]}`))
	}))
	t.Cleanup(srv.Close)

	handler := newTestHandler()
	// The test target is on loopback, which the default egress policy refuses.
	handler.SetScriptRunner(&aichecks.Runner{})

	return handler, srv.URL
}

func toolCall(t *testing.T, name string, args map[string]any) string {
	t.Helper()

	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	require.NoError(t, err)

	return string(raw)
}

// TestRunJSScriptReturnsTheCheckerResult: the tool runs the script through
// the js checker and hands its result back, and a read-only token may call it.
func TestRunJSScriptReturnsTheCheckerResult(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	handler, url := jsAuthoringHandler(t)

	body := toolCall(t, toolRunJSScript, map[string]any{
		"script": `var r = http.get(env.BASE_URL); var d = JSON.parse(r.body);
console.log("token is", secrets.TOKEN === "" ? "blank" : "set");
return { status: d.projects.length > 0 ? "up" : "down", metrics: { n: d.projects.length } };`,
		"env":     map[string]any{"BASE_URL": url},
		"secrets": []any{"TOKEN"},
	})
	rec, req := makeRequest(t, http.MethodPost, body, claimsWithScopes("mcp:read"))
	r.NoError(handler.Handle(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	resp := decodeResponse(t, rec)
	r.Nil(resp.Error)

	var result struct {
		IsError           bool               `json:"isError"`
		StructuredContent aichecks.RunResult `json:"structuredContent"`
	}

	raw, err := json.Marshal(resp.Result)
	r.NoError(err)
	r.NoError(json.Unmarshal(raw, &result))
	r.False(result.IsError)
	r.Equal("up", result.StructuredContent.Status)
	r.InDelta(1, result.StructuredContent.Metrics["n"], 0)
	r.Contains(result.StructuredContent.Output["console"], "token is blank", "secrets are names only")
}

func TestFetchPageTool(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	handler, url := jsAuthoringHandler(t)

	rec, req := makeRequest(t, http.MethodPost, toolCall(t, toolFetchPage, map[string]any{"url": url}),
		claimsWithScopes("mcp"))
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.Nil(resp.Error)

	raw, err := json.Marshal(resp.Result)
	r.NoError(err)
	r.Contains(string(raw), `"statusCode":200`)
	r.Contains(string(raw), `projects`)

	// Missing URL: a tool error, not a crash.
	rec, req = makeRequest(t, http.MethodPost, toolCall(t, toolFetchPage, map[string]any{}), claimsWithScopes("mcp"))
	r.NoError(handler.Handle(rec, req))

	raw, err = json.Marshal(decodeResponse(t, rec).Result)
	r.NoError(err)
	r.Contains(string(raw), `"isError":true`)
}

// TestJSAuthoringToolsRefuseUnauthorizedTokens: a token without an MCP scope
// is refused before any tool runs.
func TestJSAuthoringToolsRefuseUnauthorizedTokens(t *testing.T) {
	t.Parallel()

	for _, name := range []string{toolRunJSScript, toolFetchPage, toolBrowserSnapshot} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			handler, url := jsAuthoringHandler(t)

			body := toolCall(t, name, map[string]any{"script": `return {status:"up"};`, "url": url})
			rec, req := makeRequest(t, http.MethodPost, body, claimsWithScopes("checks:read"))
			r.NoError(handler.Handle(rec, req))
			r.Equal(http.StatusForbidden, rec.Code)

			rec, req = makeRequest(t, http.MethodPost, body, nil)
			r.NoError(handler.Handle(rec, req))
			r.NotEqual(http.StatusOK, rec.Code)
		})
	}
}

// TestJSAuthoringToolsAreReadScoped: the probes are not mutations, so the
// mcp:read scope gate lets them through like validate_check.
func TestJSAuthoringToolsAreReadScoped(t *testing.T) {
	t.Parallel()

	for _, name := range []string{toolRunJSScript, toolFetchPage, toolBrowserSnapshot, toolValidateCheck} {
		require.False(t, isMutationTool(name), name)
	}
}

// TestDefaultRunnerRefusesPrivateTargets: without the server's runner the
// probes go through the strict egress policy (no SSRF to loopback).
func TestDefaultRunnerRefusesPrivateTargets(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, url := jsAuthoringHandler(t)
	handler := newTestHandler()

	rec, req := makeRequest(t, http.MethodPost, toolCall(t, toolFetchPage, map[string]any{"url": url}),
		claimsWithScopes("mcp"))
	r.NoError(handler.Handle(rec, req))

	raw, err := json.Marshal(decodeResponse(t, rec).Result)
	r.NoError(err)
	r.NotContains(string(raw), "projects")
	r.Contains(string(raw), `"status":"down"`)
}
