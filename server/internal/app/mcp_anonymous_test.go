package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// This file covers spec 2026-09-16-15: `POST /api/v1/mcp` serves the MCP
// handshake (`initialize`, `notifications/initialized`) with no credentials so
// MCP directories can introspect the server, while every other method still
// answers 401 NO_TOKEN. The tests run against the REAL router (SetupRoutes),
// so they exercise the actual middleware chain rather than a hand-built copy.
//
// The substance here is the negative half: a token-less caller must not be
// able to reach tools/list, resources/list or any tools/call — directly,
// through a JSON-RPC batch, through a near-miss method name, or through an
// oversized body.

// anonCall POSTs one JSON-RPC body to /api/v1/mcp with NO Authorization
// header (and no auth cookie), returning the response and decoded body.
func (e *mcpTestEnv) anonCall(t *testing.T, rpcBody string) (httpResult, map[string]any) {
	t.Helper()
	r := require.New(t)

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, e.ts.URL+"/api/v1/mcp", strings.NewReader(rpcBody))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	res := e.do(t, req)

	decoded := map[string]any{}
	if len(bytes.TrimSpace(res.body)) > 0 {
		r.NoError(json.Unmarshal(res.body, &decoded), "body: %s", res.body)
	}

	return res, decoded
}

// requireNoTokenChallenge asserts the standard unauthenticated MCP answer:
// 401, the NO_TOKEN error code, and the RFC 9728 discovery header.
func requireNoTokenChallenge(t *testing.T, res httpResult, body map[string]any) {
	t.Helper()
	r := require.New(t)

	r.Equal(http.StatusUnauthorized, res.status, "body: %s", res.body)
	r.Equal(string(base.ErrorCodeNoToken), body["code"], "body: %s", res.body)
	r.Contains(res.header.Get("WWW-Authenticate"), "resource_metadata=")
	// Nothing from the authenticated surface may leak alongside the refusal.
	r.NotContains(string(res.body), "list_checks")
	r.NotContains(string(res.body), "\"tools\"")
}

func TestMCPAnonymousInitialize(t *testing.T) {
	t.Parallel()
	env := newMCPTestEnv(t)

	t.Run("initialize with no Authorization header succeeds", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		res, body := env.anonCall(t, mcpInitializeRPC)
		r.Equal(http.StatusOK, res.status, "body: %s", res.body)
		r.Nil(body["error"], "body: %s", res.body)

		result, ok := body["result"].(map[string]any)
		r.True(ok, "body: %s", res.body)
		r.Equal("2025-03-26", result["protocolVersion"])

		serverInfo, ok := result["serverInfo"].(map[string]any)
		r.True(ok)
		r.Equal("solidping", serverInfo["name"])
		r.NotEmpty(serverInfo["version"])

		caps, ok := result["capabilities"].(map[string]any)
		r.True(ok)
		r.Contains(caps, "tools")
		r.Contains(caps, "resources")
		r.Contains(caps, "prompts")

		// The anonymous handshake leaves no state behind: no session is minted,
		// so there is nothing an unauthenticated follow-up could reuse.
		r.Empty(res.header.Get("Mcp-Session-Id"))
	})

	t.Run("notifications/initialized is accepted anonymously", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		res, _ := env.anonCall(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
		r.Equal(http.StatusAccepted, res.status, "body: %s", res.body)
	})

	t.Run("the anonymous answer does not vary by caller", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		first, firstBody := env.anonCall(t, mcpInitializeRPC)
		r.Equal(http.StatusOK, first.status)

		// A different clientInfo must not change anything but nothing at all:
		// the response is built from static facts, never from the org or token.
		second, secondBody := env.anonCall(t,
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":`+
				`{"protocolVersion":"2025-03-26","clientInfo":{"name":"acme-probe","version":"9.9"}}}`)
		r.Equal(http.StatusOK, second.status)
		r.Equal(firstBody["result"], secondBody["result"])
	})

	t.Run("the anonymous result matches the authenticated one", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		anon, anonBody := env.anonCall(t, mcpInitializeRPC)
		r.Equal(http.StatusOK, anon.status)

		authed, authedBody := env.mcpCall(t, env.login(t), "", mcpInitializeRPC)
		r.Equal(http.StatusOK, authed.status, "body: %s", authed.body)

		// Same payload — so the anonymous response fingerprints no tenant — but
		// only the authenticated call mints a session.
		r.Equal(authedBody["result"], anonBody["result"])
		r.NotEmpty(authed.header.Get("Mcp-Session-Id"))
	})
}

func TestMCPAnonymousEverythingElseStill401(t *testing.T) {
	t.Parallel()
	env := newMCPTestEnv(t)

	// One entry per method an anonymous caller might reach for. tools/list and
	// resources/list are the product fingerprint; tools/call touches org data.
	cases := map[string]string{
		"tools/list":     mcpToolsListRPC,
		"tools/call":     mcpToolsCallRPC,
		"resources/list": `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
		"resources/read": `{"jsonrpc":"2.0","id":5,"method":"resources/read",` +
			`"params":{"uri":"solidping://checks"}}`,
		"prompts/list": `{"jsonrpc":"2.0","id":6,"method":"prompts/list"}`,
		"prompts/get": `{"jsonrpc":"2.0","id":7,"method":"prompts/get",` +
			`"params":{"name":"summarize_org_health"}}`,
		"ping": `{"jsonrpc":"2.0","id":8,"method":"ping"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res, decoded := env.anonCall(t, body)
			requireNoTokenChallenge(t, res, decoded)
		})
	}
}

func TestMCPAnonymousBypassAttempts(t *testing.T) {
	t.Parallel()
	env := newMCPTestEnv(t)

	// Every one of these is an attempt to be treated as the handshake while
	// actually asking for something else. All must land on the 401.
	cases := map[string]string{
		// JSON-RPC batch smuggling: initialize first, tools/list behind it.
		"batch hiding tools/list behind initialize": `[{"jsonrpc":"2.0","id":1,"method":"initialize"},` +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`,
		"batch of one initialize": `[{"jsonrpc":"2.0","id":1,"method":"initialize"}]`,
		// Two concatenated top-level objects in one body.
		"concatenated objects": `{"jsonrpc":"2.0","id":1,"method":"initialize"}` +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		// Near-miss method names: the match is exact, never a prefix.
		"prefix extension":   `{"jsonrpc":"2.0","id":1,"method":"initialize_and_dump"}`,
		"prefix of the name": `{"jsonrpc":"2.0","id":1,"method":"init"}`,
		"uppercased":         `{"jsonrpc":"2.0","id":1,"method":"INITIALIZE"}`,
		"mixed case":         `{"jsonrpc":"2.0","id":1,"method":"Initialize"}`,
		"leading whitespace": `{"jsonrpc":"2.0","id":1,"method":" initialize"}`,
		"trailing slash":     `{"jsonrpc":"2.0","id":1,"method":"initialize/"}`,
		// Duplicate keys: last one wins in encoding/json, for the gate and for
		// the handler alike, so the gate sees tools/list and challenges.
		"duplicate method key": `{"jsonrpc":"2.0","id":1,"method":"initialize","method":"tools/list"}`,
		// A notification-shaped envelope asking for a real method.
		"initialized then tools/list in one object": `{"jsonrpc":"2.0","method":"notifications/initialized",` +
			`"params":{"method":"tools/list"},"method":"tools/list"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res, decoded := env.anonCall(t, body)
			requireNoTokenChallenge(t, res, decoded)
		})
	}

	t.Run("oversized initialize body is refused, not served", func(t *testing.T) {
		t.Parallel()

		// Past the gate's read cap: it stops reading and defers to the auth
		// middleware rather than either serving it or reading it all.
		padding := strings.Repeat("a", 128*1024)
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26",` +
			`"clientInfo":{"name":"` + padding + `","version":"1.0"}}}`

		res, decoded := env.anonCall(t, body)
		requireNoTokenChallenge(t, res, decoded)
	})

	t.Run("initialize with a bad token is challenged, not served anonymously", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		res, decoded := env.mcpCall(t, "not-a-real-token", "", mcpInitializeRPC)
		r.Equal(http.StatusUnauthorized, res.status, "body: %s", res.body)
		r.Equal(string(base.ErrorCodeInvalidToken), decoded["code"], "body: %s", res.body)
	})

	t.Run("an anonymous handshake does not unlock a follow-up call", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		res, _ := env.anonCall(t, mcpInitializeRPC)
		r.Equal(http.StatusOK, res.status)

		// Same connection, same client, straight after the handshake.
		listRes, listBody := env.anonCall(t, mcpToolsListRPC)
		requireNoTokenChallenge(t, listRes, listBody)
	})
}
