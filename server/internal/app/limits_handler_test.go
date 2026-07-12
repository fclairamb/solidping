package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

func newLimitsServer(cfg config.RateLimitConfig) *Server {
	return &Server{
		rateLimiter: middleware.NewRateLimiter(cfg, context.Background()),
	}
}

func getLimitsResponse(t *testing.T, srv *Server, build func(*http.Request)) LimitsResponse {
	t.Helper()
	r := require.New(t)

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodGet, "/api/mgmt/limits", http.NoBody,
	)
	req.RemoteAddr = "10.20.30.40:5555"
	if build != nil {
		build(req)
	}

	w := httptest.NewRecorder()
	r.NoError(srv.getLimits(w, bunrouter.NewRequest(req)))
	r.Equal(http.StatusOK, w.Code)

	var resp LimitsResponse
	r.NoError(json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

func TestGetLimits_EchoesCallerIPAndIPBucket(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newLimitsServer(config.RateLimitConfig{
		RequestsPerMinute: 60,
		Burst:             10,
		MaxConcurrent:     5,
		TokenBucketsPerIP: 50,
	})

	resp := getLimitsResponse(t, srv, nil)
	r.Equal("10.20.30.40", resp.CallerIP)
	r.Equal(middleware.BucketKindIP, resp.CallerBucket)
	r.NotNil(resp.RateLimit.CallerRemaining)
	r.InDelta(10.0, *resp.RateLimit.CallerRemaining, 0.0001, "fresh IP reports a full burst")
}

func TestGetLimits_ReportsTokenBucketForBearerCaller(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		RequestsPerMinute: 60,
		Burst:             10,
		TokenBucketsPerIP: 50,
	}
	srv := newLimitsServer(cfg)

	// Consume a few tokens from the bearer identity's bucket via the actual
	// rate-limit middleware so /limits has real state to report.
	handler := srv.rateLimiter.RateLimit(func(w http.ResponseWriter, _ bunrouter.Request) error {
		w.WriteHeader(http.StatusOK)
		return nil
	})
	for range 3 {
		apiReq := httptest.NewRequestWithContext(
			context.Background(), http.MethodGet, "/api/v1/orgs/default/checks", http.NoBody,
		)
		apiReq.RemoteAddr = "10.20.30.40:5555"
		apiReq.Header.Set("Authorization", "Bearer my-token")
		w := httptest.NewRecorder()
		r.NoError(handler(w, bunrouter.NewRequest(apiReq)))
		r.Equal(http.StatusOK, w.Code)
	}

	// The bearer caller sees its own token bucket (3 of 10 consumed).
	tokenResp := getLimitsResponse(t, srv, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer my-token")
	})
	r.Equal("10.20.30.40", tokenResp.CallerIP)
	r.Equal(middleware.BucketKindToken, tokenResp.CallerBucket)
	r.NotNil(tokenResp.RateLimit.CallerRemaining)
	r.LessOrEqual(*tokenResp.RateLimit.CallerRemaining, 7.5, "token bucket consumption is visible")

	// An anonymous caller from the same IP sees the untouched IP bucket.
	anonResp := getLimitsResponse(t, srv, nil)
	r.Equal(middleware.BucketKindIP, anonResp.CallerBucket)
	r.NotNil(anonResp.RateLimit.CallerRemaining)
	r.InDelta(10.0, *anonResp.RateLimit.CallerRemaining, 0.0001)
}

func TestGetLimits_ResolvesXFFClientBehindTrustedProxy(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newLimitsServer(config.RateLimitConfig{
		RequestsPerMinute: 60,
		Burst:             10,
		TrustedProxies:    1,
		TokenBucketsPerIP: 50,
	})

	resp := getLimitsResponse(t, srv, func(req *http.Request) {
		req.Header.Set("X-Forwarded-For", "82.124.196.212")
	})
	r.Equal("82.124.196.212", resp.CallerIP, "single-entry XFF behind one trusted hop is the client")
	r.Equal(middleware.BucketKindIP, resp.CallerBucket)
}
