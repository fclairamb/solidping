package middleware

import (
	"net/http"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// AuditRequestMeta parks the request's provenance (client address, user agent)
// on the context so the SERVICE layer can attach it to any audit event it
// emits (spec 2026-08-21-09).
//
// This middleware deliberately does NOT emit anything. Emission belongs to the
// services that actually perform the change — an event minted from middleware
// could only say "a POST hit this path", and would miss every internal caller.
// All this does is capture two facts that are only visible at the HTTP edge.
//
// It runs for EVERY request, authenticated or not, because the request whose
// source address matters most is the failed login — which by definition never
// reaches RequireAuth.
func AuditRequestMeta(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) error {
		ctx := audit.WithRequest(
			req.Context(),
			base.ExtractRemoteAddr(req),
			req.Header.Get("User-Agent"),
		)

		return next(writer, req.WithContext(ctx))
	}
}
