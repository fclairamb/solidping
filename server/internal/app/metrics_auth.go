package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// metricsBearerPrefix is the required Authorization scheme for the /metrics
// scrape token, matched case-insensitively like every other bearer check in
// this codebase (see middleware.extractToken).
const metricsBearerPrefix = "bearer "

// metricsHandler returns the /metrics route handler, gated behind
// Prometheus.ScrapeToken (spec 2026-09-25-25):
//
//   - Token unset → 404, indistinguishable from the route never having been
//     registered at all. This is deliberate: an operator who has not opted in
//     to authenticated scraping gets the SAME "does not exist" answer as
//     Prometheus.Enabled=false, never a 401 that would confirm the endpoint is
//     there and just needs a credential — that's an easier target to notice
//     and brute-force than one that looks absent.
//   - Token set, wrong/missing bearer → 401 with a generic body. No detail
//     that would let a caller distinguish "wrong token" from "malformed
//     header" — that distinction is exactly the oracle constant-time
//     comparison exists to deny.
//   - Token set, correct bearer → the real promhttp exposition, per-org labels
//     intact (spec's point 3: once authenticated, showing them to the
//     operator's own scraper is the point).
//
// The token is read from s.config on every call, never captured into a local
// at registration time — see the call site's comment for why that matters.
func (s *Server) metricsHandler() httpx.HandlerFunc {
	promHandler := promhttp.Handler()

	return func(writer http.ResponseWriter, req *http.Request) error {
		token := s.config.Prometheus.ScrapeToken
		if token == "" {
			http.NotFound(writer, req)

			return nil
		}

		if !metricsBearerMatches(req.Header.Get("Authorization"), token) {
			return writeMetricsUnauthorized(writer)
		}

		promHandler.ServeHTTP(writer, req)

		return nil
	}
}

// metricsBearerMatches reports whether authHeader carries the scrape token as
// a bearer credential, comparing in constant time so a wrong guess and a
// malformed header take the same time to reject.
func metricsBearerMatches(authHeader, token string) bool {
	if !strings.HasPrefix(strings.ToLower(authHeader), metricsBearerPrefix) {
		return false
	}

	presented := authHeader[len(metricsBearerPrefix):]

	return subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1
}

// logMetricsScrapeTokenState logs the resolved /metrics access state at boot,
// once InitializeSystemConfig has overlaid metrics.scrape_token onto cfg —
// this runs whether the token came from the environment or the database, so
// the log always reflects what a request will actually see. A no-op when
// Prometheus is disabled outright: that state is already logged by
// SetupRoutes not registering the route at all.
func logMetricsScrapeTokenState(ctx context.Context, cfg *config.Config) {
	if !cfg.Prometheus.Enabled {
		return
	}

	if cfg.Prometheus.ScrapeToken == "" {
		slog.InfoContext(ctx,
			"metrics endpoint disabled, set metrics.scrape_token to enable scraping")

		return
	}

	slog.InfoContext(ctx, "metrics endpoint requires a bearer scrape token")
}

// writeMetricsUnauthorized writes the generic 401 body for a missing or
// incorrect scrape token, in the standard {title, code, detail} error shape.
func writeMetricsUnauthorized(writer http.ResponseWriter) error {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusUnauthorized)

	data, err := json.Marshal(base.ErrorResponse{
		Title: "Unauthorized",
		Code:  string(base.ErrorCodeUnauthorized),
	})
	if err != nil {
		return err
	}

	_, err = writer.Write(data)

	return err
}
