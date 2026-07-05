package base_test

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// TestExtractRemoteAddr covers the same fallback-order cases auth's
// extraction logic was previously (informally) exercised by, now against
// the shared, exported base.ExtractRemoteAddr — X-Forwarded-For first,
// then X-Real-IP, then the connection's RemoteAddr, then "unknown".
func TestExtractRemoteAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		xff        string
		xRealIP    string
		remoteAddr string
		want       string
	}{
		{
			name: "no headers, no RemoteAddr falls back to unknown",
			want: "unknown",
		},
		{
			name:       "RemoteAddr used when no proxy headers present",
			remoteAddr: "192.0.2.10:54321",
			want:       "192.0.2.10:54321",
		},
		{
			name:       "X-Real-IP takes priority over RemoteAddr",
			xRealIP:    "198.51.100.5",
			remoteAddr: "192.0.2.10:54321",
			want:       "198.51.100.5",
		},
		{
			name:       "X-Forwarded-For takes priority over X-Real-IP and RemoteAddr",
			xff:        "203.0.113.9",
			xRealIP:    "198.51.100.5",
			remoteAddr: "192.0.2.10:54321",
			want:       "203.0.113.9",
		},
		{
			name: "X-Forwarded-For with multiple hops uses the first (client) entry",
			xff:  "203.0.113.9, 70.41.3.18, 150.172.238.178",
			want: "203.0.113.9",
		},
		{
			name: "X-Forwarded-For entries are trimmed of surrounding whitespace",
			xff:  "  203.0.113.9  , 70.41.3.18",
			want: "203.0.113.9",
		},
		{
			name:       "empty X-Forwarded-For header falls through to X-Real-IP",
			xff:        "",
			xRealIP:    "198.51.100.5",
			remoteAddr: "192.0.2.10:54321",
			want:       "198.51.100.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			httpReq := httptest.NewRequestWithContext(t.Context(), "GET", "/heartbeat/default/my-check", nil)
			if tt.xff != "" {
				httpReq.Header.Set("X-Forwarded-For", tt.xff)
			}

			if tt.xRealIP != "" {
				httpReq.Header.Set("X-Real-IP", tt.xRealIP)
			}

			httpReq.RemoteAddr = tt.remoteAddr

			req := bunrouter.NewRequest(httpReq)
			r.Equal(tt.want, base.ExtractRemoteAddr(req))
		})
	}
}
