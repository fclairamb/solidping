package mcp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNegotiateProtocolVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		client string
		want   string
	}{
		{name: "exact match", client: protocolVersion2025_03_26, want: protocolVersion2025_03_26},
		{name: "empty falls back to latest", client: "", want: protocolVersion2025_03_26},
		{name: "future version falls back to latest", client: "2099-01-01", want: protocolVersion2025_03_26},
		{name: "unknown older falls back to latest", client: "2024-01-01", want: protocolVersion2025_03_26},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			r.Equal(tc.want, negotiateProtocolVersion(tc.client))
		})
	}
}

func TestHandleInitialize_NegotiatedVersionInResponseAndSession(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		clientVer   string
		wantInResp  string
		sessionWant string
	}{
		{
			name:        "matches supported",
			clientVer:   protocolVersion2025_03_26,
			wantInResp:  protocolVersion2025_03_26,
			sessionWant: protocolVersion2025_03_26,
		},
		{
			name:        "fallback for unsupported client version",
			clientVer:   "2099-01-01",
			wantInResp:  protocolVersion2025_03_26,
			sessionWant: protocolVersion2025_03_26,
		},
		{
			name:        "empty client version",
			clientVer:   "",
			wantInResp:  protocolVersion2025_03_26,
			sessionWant: protocolVersion2025_03_26,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			handler := newTestHandler()

			rpcReq := map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "initialize",
				"params": InitializeParams{
					ProtocolVersion: tc.clientVer,
					ClientInfo:      ClientInfo{Name: "test", Version: "1"},
				},
			}
			body, err := json.Marshal(rpcReq)
			r.NoError(err)

			rec, req := makeRequest(t, http.MethodPost, string(body), defaultClaims())
			r.NoError(handler.Handle(rec, req))
			r.Equal(http.StatusOK, rec.Code)

			resp := decodeResponse(t, rec)
			r.Nil(resp.Error)
			result, ok := resp.Result.(map[string]any)
			r.True(ok)
			r.Equal(tc.wantInResp, result["protocolVersion"])

			sessionID := rec.Header().Get("Mcp-Session-Id")
			r.NotEmpty(sessionID)
			raw, ok := handler.sessions.Load(sessionID)
			r.True(ok)
			sess, ok := raw.(*session)
			r.True(ok)
			r.Equal(tc.sessionWant, sess.protocolVersion)
		})
	}
}
