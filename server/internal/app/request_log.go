package app

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// maxLoggedUserAgentLen caps the user agent on the request log line: a client
// controls the header, and one request must not be able to bloat the logs.
const maxLoggedUserAgentLen = 256

// logRequests writes the "HTTP request" line for every request. It carries the
// client IP and the user agent so abuse of a public endpoint (the sign-up
// form, for one) can be traced to its source. The IP follows the same
// trusted-proxy resolution as the rate limiter, read live through
// trustedProxies, so a client cannot write any address it likes into the log
// with a forged X-Forwarded-For. logger is resolved per request, so a test (or
// a later slog.SetDefault) controls where the line goes.
func logRequests(next httpx.HandlerFunc, trustedProxies func() int, logger func() *slog.Logger) httpx.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) error {
		start := time.Now()
		err := next(w, req)
		duration := time.Since(start)

		userAgent := req.Header.Get("User-Agent")
		if len(userAgent) > maxLoggedUserAgentLen {
			userAgent = userAgent[:maxLoggedUserAgentLen]
		}

		attrs := []any{
			"method", req.Method,
			"path", req.URL.Path,
			"duration", duration,
			"ip", base.ExtractTrustedClientIP(req, trustedProxies()),
			"userAgent", userAgent,
		}
		// Only a failed request carries an error: "error=<nil>" on every
		// healthcheck reads as a failure in a colored terminal.
		if err != nil {
			attrs = append(attrs, "error", err)
		}

		logger().InfoContext(req.Context(), "HTTP request", attrs...)

		return err
	}
}
