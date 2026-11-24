package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// TestEventFeedRateLimitIsPerSecret: two secrets have their own budgets, the
// same secret from two addresses shares one, and the global off switch holds.
func TestEventFeedRateLimitIsPerSecret(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	off := eventFeedRateLimitConfig(config.RateLimitConfig{})
	r.Zero(off.RequestsPerMinute, "rate limiting off stays off")

	cfg := eventFeedRateLimitConfig(config.RateLimitConfig{RequestsPerMinute: 600})
	r.Equal(eventFeedRequestsPerMinute, cfg.RequestsPerMinute)

	cfg.Burst = 1
	cfg.RequestsPerMinute = 1
	limiter := middleware.NewRateLimiter(cfg, t.Context())

	router := httpx.New()
	router.Use(limiter.RateLimitRouteBy(eventFeedRateLimitKey)).
		GET("/api/v1/org-feeds/:secret/feed.xml", func(writer http.ResponseWriter, _ *http.Request) error {
			writer.WriteHeader(http.StatusOK)

			return nil
		})

	call := func(ip, secret string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/org-feeds/"+secret+"/feed.xml", nil)
		req.RemoteAddr = ip + ":1234"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		return rec.Code
	}

	r.Equal(http.StatusOK, call("1.1.1.1", "spf_a"))
	r.Equal(http.StatusTooManyRequests, call("2.2.2.2", "spf_a"), "one secret, one budget, whatever the address")
	r.Equal(http.StatusOK, call("1.1.1.1", "spf_b"), "another secret has its own budget")
}
