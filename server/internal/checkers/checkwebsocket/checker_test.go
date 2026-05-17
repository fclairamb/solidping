package checkwebsocket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// startWSServer starts an in-process WebSocket server and returns its ws:// URL.
func startWSServer(t *testing.T, handler func(*websocket.Conn)) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}

		defer func() { _ = conn.CloseNow() }()

		handler(conn)
	}))

	t.Cleanup(srv.Close)

	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestWebSocketChecker_Type(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &WebSocketChecker{}
	r.Equal(checkerdef.CheckTypeWebSocket, checker.Type())
}

func TestWebSocketChecker_Validate(t *testing.T) {
	t.Parallel()

	checker := &WebSocketChecker{}

	tests := []struct {
		name    string
		spec    checkerdef.CheckSpec
		wantErr string
	}{
		{
			name:    "missing URL",
			spec:    checkerdef.CheckSpec{Config: map[string]any{}},
			wantErr: "url: URL is required",
		},
		{
			name:    "invalid scheme",
			spec:    checkerdef.CheckSpec{Config: map[string]any{"url": "http://example.com"}},
			wantErr: "url: must start with ws:// or wss://",
		},
		{
			name:    "invalid expect regex",
			spec:    checkerdef.CheckSpec{Config: map[string]any{"url": "wss://example.com", "expect": "[invalid"}},
			wantErr: "expect: invalid regex pattern",
		},
		{
			name:    "timeout too large",
			spec:    checkerdef.CheckSpec{Config: map[string]any{"url": "wss://example.com", "timeout": "120s"}},
			wantErr: "timeout: must be > 0 and <= 60s",
		},
		{
			name: "valid — sets default name and slug",
			spec: checkerdef.CheckSpec{Config: map[string]any{"url": "wss://echo.websocket.org"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			err := checker.Validate(&tc.spec)

			if tc.wantErr != "" {
				r.Error(err)
				r.Contains(err.Error(), tc.wantErr)
			} else {
				r.NoError(err)
				r.NotEmpty(tc.spec.Name)
				r.NotEmpty(tc.spec.Slug)
			}
		})
	}
}

func TestWebSocketConfig_FromMap_GetConfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &WebSocketConfig{}
	err := cfg.FromMap(map[string]any{
		"url":             "wss://example.com",
		"send":            "ping",
		"expect":          "pong",
		"tls_skip_verify": true,
		"timeout":         "15s",
		"headers":         map[string]any{"X-Token": "abc"},
	})
	r.NoError(err)
	r.Equal("wss://example.com", cfg.URL)
	r.Equal("ping", cfg.Send)
	r.Equal("pong", cfg.Expect)
	r.True(cfg.TLSSkipVerify)
	r.Equal(15*time.Second, cfg.Timeout)
	r.Equal("abc", cfg.Headers["X-Token"])

	// Round-trip through GetConfig
	cfg2 := &WebSocketConfig{}
	r.NoError(cfg2.FromMap(cfg.GetConfig()))
	r.Equal(cfg.URL, cfg2.URL)
	r.Equal(cfg.Send, cfg2.Send)
	r.Equal(cfg.Expect, cfg2.Expect)
	r.Equal(cfg.TLSSkipVerify, cfg2.TLSSkipVerify)
	r.Equal(cfg.Timeout, cfg2.Timeout)
}

func TestWebSocketChecker_Execute_HandshakeOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &WebSocketChecker{}

	wsURL := startWSServer(t, func(conn *websocket.Conn) {
		// hold open; client will close
		ctx := context.Background()
		_, _, _ = conn.Read(ctx)
	})

	result, err := checker.Execute(context.Background(), &WebSocketConfig{URL: wsURL})
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)
	r.Equal(101, result.Output["status_code"])
	r.Nil(result.Output["received"])
}

func TestWebSocketChecker_Execute_SendAndEcho(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &WebSocketChecker{}

	wsURL := startWSServer(t, func(conn *websocket.Conn) {
		ctx := context.Background()
		_, msg, err := conn.Read(ctx)
		if err != nil {
			return
		}

		_ = conn.Write(ctx, websocket.MessageText, msg)
	})

	cfg := &WebSocketConfig{URL: wsURL, Send: echoMessage, Expect: echoMessage}
	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)
	r.Equal(echoMessage, result.Output["received"])
	r.Nil(result.Output["received_skipped"], "no frames should be skipped on an immediate match")
}

// TestWebSocketChecker_Execute_BannerThenEcho is the regression test for the original bug.
// echo.websocket.org sends a banner frame on connect before echoing — the checker must
// read past it and match the echoed payload.
func TestWebSocketChecker_Execute_BannerThenEcho(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &WebSocketChecker{}

	wsURL := startWSServer(t, func(conn *websocket.Conn) {
		ctx := context.Background()

		// Simulate the echo.websocket.org greeting frame
		_ = conn.Write(ctx, websocket.MessageText, []byte("Request served by server-1"))

		// Then act as an echo
		_, msg, err := conn.Read(ctx)
		if err != nil {
			return
		}

		_ = conn.Write(ctx, websocket.MessageText, msg)
	})

	cfg := &WebSocketConfig{URL: wsURL, Send: echoMessage, Expect: echoMessage}
	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)
	r.Equal(echoMessage, result.Output["received"])
	r.Equal(1, result.Output["received_skipped"], "banner frame must be counted as skipped")
}

// TestWebSocketChecker_Execute_AllBanners verifies that the frame cap prevents a chatty
// server from consuming the full timeout and that the check correctly reports Down.
func TestWebSocketChecker_Execute_AllBanners(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &WebSocketChecker{}

	wsURL := startWSServer(t, func(conn *websocket.Conn) {
		ctx := context.Background()

		for i := 0; i < maxReadAttempts+5; i++ {
			if err := conn.Write(ctx, websocket.MessageText, []byte("not-what-you-want")); err != nil {
				return
			}
		}
	})

	cfg := &WebSocketConfig{URL: wsURL, Send: echoMessage, Expect: echoMessage}
	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(maxReadAttempts, result.Output["received_skipped"])
	r.Contains(result.Output["error"], "response did not match expected pattern")
}

func TestWebSocketChecker_Execute_Mismatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &WebSocketChecker{}

	// Server sends a non-matching frame for each of the 8 attempts.
	wsURL := startWSServer(t, func(conn *websocket.Conn) {
		ctx := context.Background()
		_, _, _ = conn.Read(ctx)

		for i := 0; i < maxReadAttempts; i++ {
			if err := conn.Write(ctx, websocket.MessageText, []byte("goodbye")); err != nil {
				return
			}
		}
	})

	cfg := &WebSocketConfig{URL: wsURL, Send: echoMessage, Expect: echoMessage}
	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("goodbye", result.Output["received"])
	r.Contains(result.Output["error"], echoMessage)
}

func TestWebSocketChecker_Execute_DialFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &WebSocketChecker{}

	// Use a TCP port that is definitely not listening
	cfg := &WebSocketConfig{URL: "ws://127.0.0.1:1", Timeout: 2 * time.Second}
	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.NotEmpty(result.Output["error"])
}

func TestWebSocketChecker_Execute_ContextTimeout(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &WebSocketChecker{}

	wsURL := startWSServer(t, func(_ *websocket.Conn) {
		// Server accepts but never writes — forces the client read to time out
		time.Sleep(5 * time.Second)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	cfg := &WebSocketConfig{URL: wsURL, Expect: echoMessage, Timeout: 200 * time.Millisecond}
	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)
	r.True(
		result.Status == checkerdef.StatusTimeout || result.Status == checkerdef.StatusDown,
		"expected Timeout or Down, got %s", result.Status,
	)
}
