package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/tlsedge"
)

// startTLSEdge brings up in-server ACME/TLS when acme.enabled is set. It is a
// no-op otherwise, which is the default: with it off the binary behaves exactly
// as before and TLS stays with an external proxy gated on
// GET /api/v1/public/custom-domains/allowed (that endpoint is unaffected either
// way).
//
// The TLS listener is handed the SAME top-level handler as the plain listener,
// so a request arriving on a custom host over HTTPS goes through
// handlerWithCustomDomains exactly as it would over HTTP: status page plus the
// allowlisted public API, 404 for /dash0, /docs and everything else.
func (s *Server) startTLSEdge(ctx context.Context, handler http.Handler) error {
	if !s.config.ACME.Enabled {
		return nil
	}

	edge, err := tlsedge.New(&tlsedge.Options{
		ACME: s.config.ACME,
		DB:   s.dbService,
		// The instance's own hostnames get certificates too, so a self-hoster
		// enabling ACME gets HTTPS on the main host without any extra proxy.
		ReservedHosts: s.reservedCustomHosts(),
		// The ONLY other way to reach the CA: a verified, enabled, public
		// custom domain. Unverified or unknown hosts never trigger issuance.
		CustomDomainServable: s.customDomainServable,
		Handler:              handler,
		Logger:               slog.Default(),
	})
	if err != nil {
		return fmt.Errorf("failed to initialize TLS edge: %w", err)
	}

	if startErr := edge.Start(ctx); startErr != nil {
		return fmt.Errorf("failed to start TLS edge: %w", startErr)
	}

	s.tlsEdge = edge

	// Surface per-domain certificate state on the authenticated status-page
	// response so the dashboard can show why HTTPS is not live yet.
	if s.statusPagesService != nil {
		s.statusPagesService.SetCertStatusProvider(edge)
	}

	return nil
}

// customDomainServable answers the decision func's "is this a real customer
// domain?" question, reusing the same verified+enabled+public gate the
// custom-host router and the external-proxy `allowed` endpoint enforce.
func (s *Server) customDomainServable(ctx context.Context, host string) bool {
	if s.statusPagesService == nil {
		return false
	}

	return s.statusPagesService.CustomDomainServable(ctx, host)
}
