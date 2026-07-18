package checks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrTunnelInUse is returned when deleting an SSH check that other checks dial
// through. Deleting it would leave every dependent failing with a tunnel error
// on its next execution, so the API refuses with 409 and names the dependents —
// which is also how a user discovers the edge exists at all.
var ErrTunnelInUse = errors.New("check is used as an SSH tunnel by other checks")

// tunnelConfigField is the config field name reported on validation errors, so
// the dashboard can attach the message to the tunnel selector.
const tunnelConfigField = checkerdef.TunnelCheckUIDConfigKey

// validateTunnelConfig enforces every rule that makes a `tunnelCheckUid`
// reference legal. It runs on the EFFECTIVE (post-merge, post-normalize) config
// on both the create and the PATCH path — PATCH matters most: UpdateCheck never
// calls checker.Validate, so this is the only gate there.
//
// Rules (each maps to a settled design decision):
//   - the check's own type must declare SupportsTunnel — every TCP-dialing type
//     that routes its probe through the context dialer (http, tcp, the mail
//     protocols, ssl, the database drivers, and the client-library types).
//     UDP/ICMP types cannot: SSH direct-tcpip forwards TCP only.
//   - the referenced check must exist in the SAME org and not be deleted. The
//     lookup is org-scoped, so a cross-org uid simply reads as "not found".
//   - it must be an `ssh` check — the SSH check is the single home for the
//     bastion's credentials; there is no standalone tunnel resource.
//   - it must have `expected_fingerprint` set: host-key verification is
//     mandatory for tunnel use, since the tunnel carries the probe's traffic.
//   - it must not itself be tunneled: no chaining in v1, which kills cycles
//     trivially.
func (s *Service) validateTunnelConfig(
	ctx context.Context, orgUID, checkType string, effective map[string]any,
) error {
	tunnelCheckUID, ok := checkerdef.TunnelCheckUIDFrom(effective)
	if !ok {
		return nil
	}

	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckType(checkType))
	if meta == nil || !meta.SupportsTunnel {
		return checkerdef.NewConfigErrorf(
			tunnelConfigField, "check type %q cannot run through an SSH tunnel", checkType,
		)
	}

	tunnelCheck, err := s.db.GetCheck(ctx, orgUID, tunnelCheckUID)
	if err != nil || tunnelCheck == nil {
		return checkerdef.NewConfigErrorf(
			tunnelConfigField, "check %s does not exist in this organization", tunnelCheckUID,
		)
	}

	if tunnelCheck.Type != string(checkerdef.CheckTypeSSH) {
		return checkerdef.NewConfigErrorf(
			tunnelConfigField, "check %s is a %q check, only ssh checks can be used as a tunnel",
			tunnelCheckUID, tunnelCheck.Type,
		)
	}

	if fingerprint, _ := tunnelCheck.Config["expected_fingerprint"].(string); fingerprint == "" {
		return checkerdef.NewConfigErrorf(
			tunnelConfigField,
			"ssh check %s must set expected_fingerprint before it can be used as a tunnel",
			tunnelCheckUID,
		)
	}

	if _, chained := checkerdef.TunnelCheckUIDFrom(tunnelCheck.Config); chained {
		return checkerdef.NewConfigErrorf(
			tunnelConfigField,
			"ssh check %s is itself tunneled; chained tunnels are not supported",
			tunnelCheckUID,
		)
	}

	return nil
}

// assertNotUsedAsTunnel blocks deletion of an SSH check that other checks tunnel
// through, naming them so the user knows what to detach first.
func (s *Service) assertNotUsedAsTunnel(ctx context.Context, check *models.Check) error {
	if check.Type != string(checkerdef.CheckTypeSSH) {
		return nil
	}

	dependents, err := s.db.ListChecksByTunnelCheckUID(ctx, check.OrganizationUID, check.UID)
	if err != nil {
		return fmt.Errorf("failed to list tunnel dependents: %w", err)
	}

	if len(dependents) == 0 {
		return nil
	}

	return fmt.Errorf("%w: %s", ErrTunnelInUse, strings.Join(dependentLabels(dependents), ", "))
}

// dependentLabels renders dependents by slug (falling back to uid for the
// slug-less edge case).
func dependentLabels(checks []*models.Check) []string {
	labels := make([]string, 0, len(checks))

	for _, check := range checks {
		if check.Slug != nil && *check.Slug != "" {
			labels = append(labels, *check.Slug)

			continue
		}

		labels = append(labels, check.UID)
	}

	return labels
}
