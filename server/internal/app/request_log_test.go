package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var errRequestLogBoom = errors.New("boom")

// TestRequestLogCarriesClientIPAndUserAgent pins the fields of the
// "HTTP request" line: the client IP (trusted-proxy resolution, so a forged
// X-Forwarded-For is ignored unless a proxy hop is trusted) and the user
// agent, truncated.
func TestRequestLogCarriesClientIPAndUserAgent(t *testing.T) {
	t.Parallel()

	longUA := strings.Repeat("a", maxLoggedUserAgentLen+50)

	tests := []struct {
		name           string
		trustedProxies int
		xff            string
		userAgent      string
		handlerErr     error
		wantIP         string
		wantUserAgent  string
	}{
		{
			name: "direct client", wantIP: "198.51.100.7",
			userAgent: "acme-agent/1.0", wantUserAgent: "acme-agent/1.0",
		},
		{
			name: "forged forwarding header without a trusted proxy", xff: "203.0.113.9",
			wantIP: "198.51.100.7", userAgent: "acme-agent/1.0", wantUserAgent: "acme-agent/1.0",
		},
		{
			name: "behind one trusted proxy", trustedProxies: 1, xff: "203.0.113.9",
			wantIP: "203.0.113.9", userAgent: "acme-agent/1.0", wantUserAgent: "acme-agent/1.0",
		},
		{
			name: "oversized user agent", wantIP: "198.51.100.7",
			userAgent: longUA, wantUserAgent: longUA[:maxLoggedUserAgentLen],
		},
		{
			name: "failed request", wantIP: "198.51.100.7", handlerErr: errRequestLogBoom,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))

			handler := logRequests(
				func(http.ResponseWriter, *http.Request) error { return tc.handlerErr },
				func() int { return tc.trustedProxies },
				func() *slog.Logger { return logger },
			)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/register", nil)
			req.RemoteAddr = "198.51.100.7:51234"
			req.Header.Set("User-Agent", tc.userAgent)

			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}

			r.ErrorIs(handler(httptest.NewRecorder(), req), tc.handlerErr)

			var line map[string]any
			r.NoError(json.Unmarshal(buf.Bytes(), &line))
			r.Equal("HTTP request", line["msg"])
			r.Equal("/api/v1/auth/register", line["path"])
			r.Equal(tc.wantIP, line["ip"])
			r.Equal(tc.wantUserAgent, line["userAgent"])

			if tc.handlerErr != nil {
				r.Equal("boom", line["error"])
			} else {
				r.NotContains(line, "error")
			}
		})
	}
}
