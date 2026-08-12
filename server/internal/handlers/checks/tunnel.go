package checks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// ErrTunnelInUse is returned when deleting an SSH check that other checks dial
// through. Deleting it would leave every dependent failing with a tunnel error
// on its next execution, so the API refuses with 409 and names the dependents —
// which is also how a user discovers the edge exists at all.
var ErrTunnelInUse = errors.New("check is used as an SSH tunnel by other checks")

// ErrTunnelRegionNarrowed is returned when an SSH check used as a tunnel is
// edited in a way that would leave a dependent without a tunnel in one of its
// regions — removing a private region the dependent runs in, or going
// sealed-only while a cloud dependent relies on it. Rejected with 409 and the
// dependent labels so the user knows what to detach or re-region first.
var ErrTunnelRegionNarrowed = errors.New(
	"check is used as an SSH tunnel; this change would leave dependents without a tunnel in their region")

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
//   - REGION RULES (spec 2026-07-18-07, decisions 1–2): every private region of
//     the dependent must be one the SSH check is allocated to (so its secrets
//     are already sealed to that region's agents); and if the dependent runs in
//     any cloud region, the SSH check must be server-resolvable there (not
//     sealed-only). dependentRegions is the dependent's resolved region set —
//     the region rules fire on both create and update (setting `tunnelCheckUid`
//     OR changing the dependent's regions).
func (s *Service) validateTunnelConfig(
	ctx context.Context, orgUID, checkType string, effective map[string]any, dependentRegions []string,
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

	if reason := tunnelRegionViolation(tunnelCheck, dependentRegions); reason != "" {
		return checkerdef.NewConfigErrorf(tunnelConfigField, "%s", reason)
	}

	return nil
}

// tunnelRegionViolation returns a human-readable reason (naming the region and
// the fix) when the SSH check cannot serve a dependent running in
// dependentRegions, or "" when the pair is region-compatible.
//
// Two rules, both settled in the spec's Decisions:
//   - decision 1: every PRIVATE region (`@…`) the dependent runs in must be a
//     region the SSH check is itself allocated to. That is what guarantees the
//     SSH check's secrets are already sealed to that region's agents, so the
//     server ships the sealed envelope verbatim and never re-seals anything.
//   - decision 2: if the dependent runs in any CLOUD region, the SSH check must
//     be server-resolvable there — i.e. not sealed-only. A sealed-only SSH
//     check (allocated exclusively to private regions; config_private NULL) can
//     never be opened by the server for the cloud dispatch path.
func tunnelRegionViolation(sshCheck *models.Check, dependentRegions []string) string {
	sshRegions := make(map[string]struct{}, len(sshCheck.Regions))
	for _, region := range sshCheck.Regions {
		sshRegions[region] = struct{}{}
	}

	hasCloud := false

	for _, region := range dependentRegions {
		if !regions.IsPrivateRegion(region) {
			hasCloud = true

			continue
		}

		if _, covered := sshRegions[region]; !covered {
			return fmt.Sprintf(
				"ssh check %s must be allocated to region %s to be used as a tunnel there",
				checkLabel(sshCheck), regionDisplay(region),
			)
		}
	}

	if hasCloud && isSealedOnly(sshCheck) {
		return fmt.Sprintf(
			"ssh check %s stores its credentials sealed to private-region agents only, so it "+
				"cannot be used as a tunnel by a check that runs in a cloud region — give the ssh "+
				"check a matching cloud region, or restrict this check to the ssh check's private regions",
			checkLabel(sshCheck),
		)
	}

	return ""
}

// isSealedOnly reports whether an SSH check stores its secrets sealed to
// private-region agents only (config_sealed set, config_private NULL) — the
// state in which the server cannot resolve the tunnel for a cloud dispatch.
func isSealedOnly(check *models.Check) bool {
	sealed := check.ConfigSealed != nil && *check.ConfigSealed != ""
	private := check.ConfigPrivate != nil && *check.ConfigPrivate != ""

	return sealed && !private
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

// assertTunnelRegionsStillCover guards a region-narrowing (or going-sealed-only)
// update of an SSH check that is used as a tunnel: after the update takes
// effect, every dependent must still be region-compatible with it. It re-runs
// the same region rules validateTunnelConfig applies to a dependent, but from
// the SSH check's side, against the post-update `check` state (its new regions
// and new sealed-status). Any dependent left stranded → reject naming it.
//
// check must already carry the regions and sealing state it will have AFTER the
// update (the UpdateCheck flow resolves regions and re-seals before calling
// this), so a removed private region or a freshly sealed-only check is caught.
func (s *Service) assertTunnelRegionsStillCover(ctx context.Context, check *models.Check) error {
	if check.Type != string(checkerdef.CheckTypeSSH) {
		return nil
	}

	dependents, err := s.db.ListChecksByTunnelCheckUID(ctx, check.OrganizationUID, check.UID)
	if err != nil {
		return fmt.Errorf("failed to list tunnel dependents: %w", err)
	}

	stranded := make([]string, 0, len(dependents))

	for _, dependent := range dependents {
		if tunnelRegionViolation(check, dependent.Regions) != "" {
			stranded = append(stranded, checkLabel(dependent))
		}
	}

	if len(stranded) == 0 {
		return nil
	}

	return fmt.Errorf("%w: %s", ErrTunnelRegionNarrowed, strings.Join(stranded, ", "))
}

// dependentLabels renders dependents by slug (falling back to uid for the
// slug-less edge case).
func dependentLabels(checks []*models.Check) []string {
	labels := make([]string, 0, len(checks))

	for _, check := range checks {
		labels = append(labels, checkLabel(check))
	}

	return labels
}

// checkLabel is the best human label for a check in an error message: slug,
// falling back to uid for the slug-less edge case.
func checkLabel(check *models.Check) string {
	if check.Slug != nil && *check.Slug != "" {
		return *check.Slug
	}

	return check.UID
}

// regionDisplay renders a region for an error message. Private regions are
// stored org-relatively (`@<slug>`) and cloud regions verbatim, so both are
// already display-ready; the legacy fully-qualified `@<org>/<slug>` spelling is
// still folded down here so a row migration 012 has not reached (or an operator
// hand-edit) never leaks another org's slug into a user-facing message.
func regionDisplay(region string) string {
	if _, slug, ok := regions.ParseLegacyPrivateRegion(region); ok {
		return regions.PrivateRegionSlug(slug)
	}

	return region
}
