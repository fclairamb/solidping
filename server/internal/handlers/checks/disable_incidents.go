package checks

import (
	"context"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// errResolveOpenNeedsDisable rejects resolveOpenIncidents without enabled:false.
// Listed in isCheckFieldValidationError so it is a 400.
var errResolveOpenNeedsDisable = errors.New("resolveOpenIncidents requires enabled: false")

// CheckDisableIncidentResolver closes the open incidents of a check that was
// just disabled by hand (spec 2026-10-23-01). It is an interface because the
// implementation lives in handlers/incidents, which this package cannot import.
type CheckDisableIncidentResolver interface {
	ResolveOnCheckDisable(ctx context.Context, orgUID, checkUID string) ([]string, error)
}

// SetCheckDisableIncidentResolver wires the resolver. Every service serving a
// check write path (HTTP routes, MCP) must set it, or resolveOpenIncidents is
// silently ignored.
func (s *Service) SetCheckDisableIncidentResolver(resolver CheckDisableIncidentResolver) {
	s.disableResolver = resolver
}

func validateResolveOpenIncidents(req *UpdateCheckRequest) error {
	if req.ResolveOpenIncidents != nil && *req.ResolveOpenIncidents &&
		(req.Enabled == nil || *req.Enabled) {
		return errResolveOpenNeedsDisable
	}

	return nil
}

// resolveIncidentsOnDisable fires on any write that sets enabled:false with
// resolveOpenIncidents:true, not only on a true to false transition, so a retry
// after a failed resolve still closes them.
func (s *Service) resolveIncidentsOnDisable(
	ctx context.Context, check *models.Check, req *UpdateCheckRequest,
) error {
	if s.disableResolver == nil || req.ResolveOpenIncidents == nil || !*req.ResolveOpenIncidents ||
		req.Enabled == nil || *req.Enabled {
		return nil
	}

	if _, err := s.disableResolver.ResolveOnCheckDisable(ctx, check.OrganizationUID, check.UID); err != nil {
		return fmt.Errorf("resolve open incidents: %w", err)
	}

	return nil
}
