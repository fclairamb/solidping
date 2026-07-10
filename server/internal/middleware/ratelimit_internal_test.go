package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractIPFromXFF(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		xff            string
		trustedProxies int
		want           string
	}{
		{
			// The honest single-entry case behind one proxy that overwrote
			// the header: the sole entry IS the client. The pre-fix code
			// returned "" here, defeating per-client bucketing entirely.
			name:           "single entry, one trusted hop",
			xff:            "82.124.196.212",
			trustedProxies: 1,
			want:           "82.124.196.212",
		},
		{
			// Behind a proxy that appends rather than strips (ALB, default
			// nginx): the client-supplied left entry must be ignored — the
			// trusted hop appended the real client as the last entry. The
			// pre-fix code returned the spoofed "6.6.6.6" (bucket-choosing
			// bypass).
			name:           "spoofed left entry, one trusted hop",
			xff:            "6.6.6.6, 82.124.196.212",
			trustedProxies: 1,
			want:           "82.124.196.212",
		},
		{
			// CDN → LB, both trusted: LB appended the CDN's peer (the
			// client), CDN appended its own peer. Client is 2nd from right.
			name:           "two entries, two trusted hops",
			xff:            "82.124.196.212, 10.1.2.3",
			trustedProxies: 2,
			want:           "82.124.196.212",
		},
		{
			name:           "spoofed entry ahead of two trusted hops",
			xff:            "6.6.6.6, 82.124.196.212, 10.1.2.3",
			trustedProxies: 2,
			want:           "82.124.196.212",
		},
		{
			name:           "three entries, one trusted hop takes the last",
			xff:            "6.6.6.6, 7.7.7.7, 82.124.196.212",
			trustedProxies: 1,
			want:           "82.124.196.212",
		},
		{
			// More trusted hops than entries: clamp to the left-most entry —
			// the best available answer when upstream hops collapsed or
			// stripped the chain.
			name:           "fewer entries than trusted hops clamps to first",
			xff:            "82.124.196.212",
			trustedProxies: 3,
			want:           "82.124.196.212",
		},
		{
			name:           "whitespace around entries is trimmed",
			xff:            " 6.6.6.6 , 82.124.196.212 ",
			trustedProxies: 1,
			want:           "82.124.196.212",
		},
		{
			name:           "zero trusted proxies yields nothing",
			xff:            "82.124.196.212",
			trustedProxies: 0,
			want:           "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			r.Equal(tc.want, extractIPFromXFF(tc.xff, tc.trustedProxies))
		})
	}
}

func newIPRequest(t *testing.T, remoteAddr, xff, xri string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/x", http.NoBody)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if xri != "" {
		req.Header.Set("X-Real-IP", xri)
	}
	return req
}

func TestExtractIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		remoteAddr     string
		xff            string
		xri            string
		trustedProxies int
		want           string
	}{
		{
			name:           "no proxies trusted ignores headers",
			remoteAddr:     "10.0.0.9:4242",
			xff:            "82.124.196.212",
			xri:            "82.124.196.212",
			trustedProxies: 0,
			want:           "10.0.0.9",
		},
		{
			name:           "single XFF entry behind one trusted hop",
			remoteAddr:     "10.0.0.9:4242",
			xff:            "82.124.196.212",
			trustedProxies: 1,
			want:           "82.124.196.212",
		},
		{
			name:           "XFF wins over X-Real-IP when both present",
			remoteAddr:     "10.0.0.9:4242",
			xff:            "82.124.196.212",
			xri:            "6.6.6.6",
			trustedProxies: 1,
			want:           "82.124.196.212",
		},
		{
			name:           "X-Real-IP used only when XFF is absent",
			remoteAddr:     "10.0.0.9:4242",
			xri:            " 82.124.196.212 ",
			trustedProxies: 1,
			want:           "82.124.196.212",
		},
		{
			// XFF present but yields nothing usable (whitespace entry):
			// fall back to RemoteAddr, NOT to the equally client-
			// controllable X-Real-IP.
			name:           "unparsable XFF falls back to RemoteAddr not X-Real-IP",
			remoteAddr:     "10.0.0.9:4242",
			xff:            " , ",
			xri:            "6.6.6.6",
			trustedProxies: 1,
			want:           "10.0.0.9",
		},
		{
			name:           "no headers falls back to RemoteAddr host",
			remoteAddr:     "10.0.0.9:4242",
			trustedProxies: 1,
			want:           "10.0.0.9",
		},
		{
			name:           "RemoteAddr without port returned as-is",
			remoteAddr:     "10.0.0.9",
			trustedProxies: 0,
			want:           "10.0.0.9",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			req := newIPRequest(t, tc.remoteAddr, tc.xff, tc.xri)
			r.Equal(tc.want, extractIP(req, tc.trustedProxies))
		})
	}
}
