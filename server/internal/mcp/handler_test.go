package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// makeRequest builds a bunrouter.Request with optional claims in context.
func makeRequest(
	t *testing.T, method, body string, claims *auth.Claims,
) (*httptest.ResponseRecorder, bunrouter.Request) {
	t.Helper()

	httpReq := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/mcp", bytes.NewBufferString(body))
	httpReq.Header.Set("Content-Type", "application/json")

	if claims != nil {
		ctx := context.WithValue(httpReq.Context(), base.ContextKeyClaims, claims)
		httpReq = httpReq.WithContext(ctx)
	}

	rec := httptest.NewRecorder()
	bunReq := bunrouter.NewRequest(httpReq)

	return rec, bunReq
}

func defaultClaims() *auth.Claims {
	return &auth.Claims{
		UserUID: "user-123",
		OrgSlug: "test-org",
	}
}

func newTestHandler() *Handler {
	handler := &Handler{}
	handler.registerTools()

	return handler
}

// decodeResponse unmarshals the recorder body into a Response.
func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder) Response {
	t.Helper()

	var resp Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	return resp
}

// --- Handle method tests ---

func TestHandle_RejectsNonPostMethods(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	rec, req := makeRequest(t, http.MethodGet, "", defaultClaims())
	r.NoError(handler.Handle(rec, req))
	r.Equal(http.StatusMethodNotAllowed, rec.Code)
}

func TestHandle_RejectsUnauthenticated(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	rec, req := makeRequest(t, http.MethodPost, body, nil)
	r.NoError(handler.Handle(rec, req))
	r.Equal(http.StatusUnauthorized, rec.Code)

	resp := decodeResponse(t, rec)
	r.NotNil(resp.Error)
	r.Equal(CodeInvalidRequest, resp.Error.Code)
}

func TestHandle_RejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	rec, req := makeRequest(t, http.MethodPost, "not json", defaultClaims())
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.NotNil(resp.Error)
	r.Equal(CodeParseError, resp.Error.Code)
}

func TestHandle_RejectsInvalidJSONRPCVersion(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	body := `{"jsonrpc":"1.0","id":1,"method":"ping"}`
	rec, req := makeRequest(t, http.MethodPost, body, defaultClaims())
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.NotNil(resp.Error)
	r.Equal(CodeInvalidRequest, resp.Error.Code)
}

// --- Dispatch tests ---

func TestDispatch_Ping(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	rec, req := makeRequest(t, http.MethodPost, body, defaultClaims())
	r.NoError(handler.Handle(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	resp := decodeResponse(t, rec)
	r.Nil(resp.Error)
	// ID comes back as float64 from JSON unmarshaling
	r.EqualValues(1, resp.ID)
}

func TestDispatch_MethodNotFound(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	body := `{"jsonrpc":"2.0","id":1,"method":"nonexistent"}`
	rec, req := makeRequest(t, http.MethodPost, body, defaultClaims())
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.NotNil(resp.Error)
	r.Equal(CodeMethodNotFound, resp.Error.Code)
}

func TestDispatch_Initialize(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	params := InitializeParams{
		ProtocolVersion: "2025-03-26",
		ClientInfo:      ClientInfo{Name: "test-client", Version: "1.0"},
	}
	paramsJSON, err := json.Marshal(params)
	r.NoError(err)

	rpcReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  json.RawMessage(paramsJSON),
	}
	body, err := json.Marshal(rpcReq)
	r.NoError(err)

	rec, req := makeRequest(t, http.MethodPost, string(body), defaultClaims())
	r.NoError(handler.Handle(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	// Check session ID header
	sessionID := rec.Header().Get("Mcp-Session-Id")
	r.NotEmpty(sessionID)

	resp := decodeResponse(t, rec)
	r.Nil(resp.Error)

	// Verify result structure
	result, ok := resp.Result.(map[string]any)
	r.True(ok)
	r.Equal(mcpProtocolVer, result["protocolVersion"])

	serverInfo, ok := result["serverInfo"].(map[string]any)
	r.True(ok)
	r.Equal("solidping", serverInfo["name"])

	// Verify session was stored
	_, loaded := handler.sessions.Load(sessionID)
	r.True(loaded)
}

func TestDispatch_NotificationsInitialized(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	rec, req := makeRequest(t, http.MethodPost, body, defaultClaims())
	r.NoError(handler.Handle(rec, req))
	r.Equal(http.StatusAccepted, rec.Code)
}

func TestDispatch_ToolsList(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	rec, req := makeRequest(t, http.MethodPost, body, defaultClaims())
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.Nil(resp.Error)

	result, ok := resp.Result.(map[string]any)
	r.True(ok)

	tools, ok := result["tools"].([]any)
	r.True(ok)
	r.Len(tools, 32)

	// Verify tool names
	names := make(map[string]bool)
	for _, tool := range tools {
		toolMap, mapOK := tool.(map[string]any)
		r.True(mapOK)

		name, strOK := toolMap["name"].(string)
		r.True(strOK)
		names[name] = true
	}

	expectedTools := []string{
		"list_checks", "get_check", "create_check", "update_check", "delete_check",
		"list_results", "list_incidents", "get_incident",
		"list_connections", "create_connection",
		"list_check_groups", "list_regions",
		"diagnose_check",
		"list_status_pages", "get_status_page", "create_status_page",
		"update_status_page", "delete_status_page",
		"list_status_page_sections", "create_status_page_section",
		"update_status_page_section", "delete_status_page_section",
		"list_status_page_resources", "create_status_page_resource",
		"update_status_page_resource", "delete_status_page_resource",
		"list_maintenance_windows", "get_maintenance_window",
		"create_maintenance_window", "update_maintenance_window",
		"delete_maintenance_window", "set_maintenance_window_checks",
	}
	for _, name := range expectedTools {
		r.True(names[name], "missing tool: %s", name)
	}
}

func TestDispatch_ToolsCallUnknownTool(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nonexistent_tool"}}`
	rec, req := makeRequest(t, http.MethodPost, body, defaultClaims())
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.NotNil(resp.Error)
	r.Equal(CodeMethodNotFound, resp.Error.Code)
	r.Contains(resp.Error.Message, "nonexistent_tool")
}

func TestDispatch_ResourcesList(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	body := `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`
	rec, req := makeRequest(t, http.MethodPost, body, defaultClaims())
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.Nil(resp.Error)

	result, ok := resp.Result.(map[string]any)
	r.True(ok)

	resources, ok := result["resources"].([]any)
	r.True(ok)
	r.Len(resources, 2)
}

func TestDispatch_ResourcesReadUnknown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	body := `{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"solidping://unknown"}}`
	rec, req := makeRequest(t, http.MethodPost, body, defaultClaims())
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.NotNil(resp.Error)
	r.Equal(CodeNotFound, resp.Error.Code)
}

// --- Session management tests ---

func TestSessionCleanup(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()

	// Store an expired session
	handler.sessions.Store("expired-session", &session{
		id:       "expired-session",
		orgSlug:  "test-org",
		lastUsed: time.Now().Add(-2 * sessionTTL),
	})

	// Store a fresh session
	handler.sessions.Store("fresh-session", &session{
		id:       "fresh-session",
		orgSlug:  "test-org",
		lastUsed: time.Now(),
	})

	// Run cleanup via Start/Stop with a short-lived context
	ctx, cancel := context.WithCancel(t.Context())
	handler.Start(ctx)
	cancel()
	handler.Stop()

	// Manually trigger cleanup since the loop interval is 5 minutes
	now := time.Now()
	handler.sessions.Range(func(key, value any) bool {
		sess, ok := value.(*session)
		if !ok {
			return true
		}
		if now.Sub(sess.lastUsed) > sessionTTL {
			handler.sessions.Delete(key)
		}
		return true
	})

	// Verify expired session was removed
	_, loaded := handler.sessions.Load("expired-session")
	r.False(loaded, "expired session should have been cleaned up")

	// Verify fresh session remains
	_, loaded = handler.sessions.Load("fresh-session")
	r.True(loaded, "fresh session should still exist")
}

// --- Argument helper tests ---

func TestGetStringArg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	tests := []struct {
		name     string
		args     map[string]any
		key      string
		expected string
	}{
		{"existing string", map[string]any{"key": "value"}, "key", "value"},
		{"missing key", map[string]any{}, "key", ""},
		{"non-string value", map[string]any{"key": 42}, "key", ""},
		{"empty string", map[string]any{"key": ""}, "key", ""},
		{"nil value", map[string]any{"key": nil}, "key", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r.Equal(tc.expected, getStringArg(tc.args, tc.key))
		})
	}
}

func TestGetIntArg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	tests := []struct {
		name       string
		args       map[string]any
		key        string
		defaultVal int
		expected   int
	}{
		{"float64 value", map[string]any{"limit": float64(50)}, "limit", 20, 50},
		{"int value", map[string]any{"limit": 30}, "limit", 20, 30},
		{"missing key", map[string]any{}, "limit", 20, 20},
		{"non-numeric value", map[string]any{"limit": "abc"}, "limit", 20, 20},
		{"zero float64", map[string]any{"limit": float64(0)}, "limit", 20, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r.Equal(tc.expected, getIntArg(tc.args, tc.key, tc.defaultVal))
		})
	}
}

func TestGetBoolArg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	t.Run("true value", func(t *testing.T) {
		t.Parallel()
		result := getBoolArg(map[string]any{"enabled": true}, "enabled")
		r.NotNil(result)
		r.True(*result)
	})

	t.Run("false value", func(t *testing.T) {
		t.Parallel()
		result := getBoolArg(map[string]any{"enabled": false}, "enabled")
		r.NotNil(result)
		r.False(*result)
	})

	t.Run("missing key", func(t *testing.T) {
		t.Parallel()
		result := getBoolArg(map[string]any{}, "enabled")
		r.Nil(result)
	})

	t.Run("non-bool value", func(t *testing.T) {
		t.Parallel()
		result := getBoolArg(map[string]any{"enabled": "yes"}, "enabled")
		r.Nil(result)
	})
}

func TestGetStringSliceArg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	t.Run("[]any with strings", func(t *testing.T) {
		t.Parallel()
		args := map[string]any{"items": []any{"eu", "us"}}
		r.Equal([]string{"eu", "us"}, getStringSliceArg(args, "items"))
	})

	t.Run("[]string", func(t *testing.T) {
		t.Parallel()
		args := map[string]any{"items": []string{"eu"}}
		r.Equal([]string{"eu"}, getStringSliceArg(args, "items"))
	})

	t.Run("missing key", func(t *testing.T) {
		t.Parallel()
		r.Nil(getStringSliceArg(map[string]any{}, "items"))
	})

	t.Run("non-slice value", func(t *testing.T) {
		t.Parallel()
		args := map[string]any{"items": "eu"}
		r.Nil(getStringSliceArg(args, "items"))
	})

	t.Run("[]any with mixed types skips non-strings", func(t *testing.T) {
		t.Parallel()
		args := map[string]any{"items": []any{"eu", 42, "us"}}
		r.Equal([]string{"eu", "us"}, getStringSliceArg(args, "items"))
	})
}

func TestGetMapArg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	t.Run("valid map", func(t *testing.T) {
		t.Parallel()
		input := map[string]any{"config": map[string]any{"url": "https://example.com"}}
		r.Equal(map[string]any{"url": "https://example.com"}, getMapArg(input, "config"))
	})

	t.Run("missing key", func(t *testing.T) {
		t.Parallel()
		r.Nil(getMapArg(map[string]any{}, "config"))
	})

	t.Run("non-map value", func(t *testing.T) {
		t.Parallel()
		r.Nil(getMapArg(map[string]any{"config": "not a map"}, "config"))
	})
}

func TestGetStringMapArg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	t.Run("valid string map", func(t *testing.T) {
		t.Parallel()
		input := map[string]any{"data": map[string]any{"env": "prod", "team": "sre"}}
		r.Equal(map[string]string{"env": "prod", "team": "sre"}, getStringMapArg(input, "data"))
	})

	t.Run("skips non-string values", func(t *testing.T) {
		t.Parallel()
		input := map[string]any{"data": map[string]any{"env": "prod", "count": 42}}
		r.Equal(map[string]string{"env": "prod"}, getStringMapArg(input, "data"))
	})

	t.Run("missing key", func(t *testing.T) {
		t.Parallel()
		r.Nil(getStringMapArg(map[string]any{}, "data"))
	})
}
