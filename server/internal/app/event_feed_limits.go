package app

import (
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// eventFeedRequestsPerMinute is the per-secret budget of a private org event
// feed (spec 2026-11-24-12). A reader polls every few minutes; a wall of
// dashboards sharing one URL a few times a minute. The response is
// cacheable for a minute, so anything above this is a misbehaving client.
const eventFeedRequestsPerMinute = 30

// eventFeedRateLimitConfig derives the feed limiter from the server's own,
// keeping its trusted proxies and its global off switch.
func eventFeedRateLimitConfig(base config.RateLimitConfig) config.RateLimitConfig {
	if base.RequestsPerMinute == 0 {
		return base
	}

	return config.RateLimitConfig{
		RequestsPerMinute: eventFeedRequestsPerMinute,
		Burst:             eventFeedRequestsPerMinute / 3,
		TrustedProxies:    base.TrustedProxies,
	}
}

// eventFeedRateLimitKey buckets feed reads by secret. The hash, not the
// secret, names the bucket, so the limiter's memory never holds a working
// URL.
func eventFeedRateLimitKey(req *http.Request) string {
	secret := httpx.Param(req, "secret")
	if secret == "" {
		return ""
	}

	return "feed:" + models.HashOrgEventFeedSecret(secret)
}
