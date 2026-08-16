package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/entitlements"
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

// SaaS-mode seed env vars. koanf collapses every underscore in an SP_*
// name into a dot, so these multi-word keys are read manually here
// (same pattern as SP_RUN_MODE / SP_SHUTDOWN_TIMEOUT in config.Load).
const (
	envEntitlementsServiceToken  = "SP_ENTITLEMENTS_SERVICE_TOKEN"
	envEntitlementsUpgradeURL    = "SP_ENTITLEMENTS_UPGRADE_URL_TEMPLATE"
	envEntitlementsAdminWrites   = "SP_ENTITLEMENTS_ADMIN_WRITES_ENABLED"
	envEntitlementsBillingSecret = "SP_ENTITLEMENTS_BILLING_INBOUND_SECRET"
	// envEntitlementsUpgradeTokenSecret carries the DEDICATED HS256 key that
	// signs the `#bt=` upgrade token. Deliberately distinct from the inbound
	// bearer above: a leak of a bearer must not also be the power to mint an
	// upgrade token for any org. Mirrors billing's BILLING_UPGRADE_TOKEN_SECRET.
	envEntitlementsUpgradeTokenSecret = "SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET"
	// Ordered {id, secret} JSON key sets for the signed service channels, one
	// per direction (see internal/servicesig). The billing service holds the
	// mirror image as BILLING_SIGNING_KEYS_OUTBOUND / _INBOUND.
	envEntitlementsServiceSigningKeys  = "SP_ENTITLEMENTS_SERVICE_SIGNING_KEYS"
	envEntitlementsOutboundSigningKeys = "SP_ENTITLEMENTS_OUTBOUND_SIGNING_KEYS"
	envEntitlementsAllowLegacyToken    = "SP_ENTITLEMENTS_ALLOW_LEGACY_SERVICE_TOKEN"
)

// SeedSaaSEntitlements wires the system parameters the entitlements handler
// needs in SaaS mode (where the separate billing service drives upgrades).
//
// In SaaS mode the entitlements endpoint authenticates the billing service
// with a shared `entitlements.service_token`, and the dashboard renders an
// "Upgrade" link built from `entitlements.upgrade_url_template`. Those live
// in the DB `parameters` table; this seeds them from env so an operator (or
// `make dev-saas`) configures them declaratively. Each value is upserted
// only when its env var is set, so partial configuration is fine and
// later API edits are not clobbered on the next boot when env is unset.
//
// No-op outside SaaS mode — self-hosted installs never touch billing.
func (s *Server) SeedSaaSEntitlements(ctx context.Context) error {
	if s.config.Deployment.Mode != config.DeploymentModeSaaS {
		return nil
	}

	if token := os.Getenv(envEntitlementsServiceToken); token != "" {
		if err := s.dbService.SetSystemParameter(ctx, entitlements.ParamServiceToken, token, true); err != nil {
			return err
		}
		slog.InfoContext(ctx, "SaaS: seeded entitlements service token", "key", entitlements.ParamServiceToken)
	}

	if tmpl := os.Getenv(envEntitlementsUpgradeURL); tmpl != "" {
		if err := s.dbService.SetSystemParameter(ctx, entitlements.ParamUpgradeURLTemplate, tmpl, false); err != nil {
			return err
		}
		slog.InfoContext(ctx, "SaaS: seeded entitlements upgrade URL template",
			"key", entitlements.ParamUpgradeURLTemplate, "template", tmpl)
	}

	if secret := os.Getenv(envEntitlementsBillingSecret); secret != "" {
		if err := s.dbService.SetSystemParameter(ctx, entitlements.ParamBillingInboundSecret, secret, true); err != nil {
			return err
		}
		slog.InfoContext(ctx, "SaaS: seeded entitlements billing inbound secret",
			"key", entitlements.ParamBillingInboundSecret)
	}

	if secret := os.Getenv(envEntitlementsUpgradeTokenSecret); secret != "" {
		if err := s.dbService.SetSystemParameter(
			ctx, entitlements.ParamBillingUpgradeTokenSecret, secret, true,
		); err != nil {
			return err
		}
		slog.InfoContext(ctx, "SaaS: seeded entitlements billing upgrade token secret",
			"key", entitlements.ParamBillingUpgradeTokenSecret)
	}

	if err := s.seedSigningKeys(ctx); err != nil {
		return err
	}

	s.checkBillingSecretsAreDistinct(ctx)

	if raw := os.Getenv(envEntitlementsAdminWrites); raw != "" {
		enabled, parseErr := strconv.ParseBool(raw)
		switch {
		case parseErr != nil:
			slog.WarnContext(ctx, "SaaS: ignoring unparsable "+envEntitlementsAdminWrites,
				"value", raw, "error", parseErr)
		default:
			if err := s.dbService.SetSystemParameter(
				ctx, entitlements.ParamAdminWritesEnabled, enabled, false,
			); err != nil {
				return err
			}
		}
	}

	return nil
}

// checkBillingSecretsAreDistinct logs an ERROR when the upgrade-token secret
// and the inbound bearer hold the same value. Equal secrets are
// indistinguishable from the unsplit state, and a deployment that believes it
// has split when it has not is worse than one that knows it has not.
//
// Deliberately NOT a startup failure: the value is "correct but not yet
// rotated", and a hard failure there would strand an otherwise healthy prod
// boot.
func (s *Server) checkBillingSecretsAreDistinct(ctx context.Context) {
	upgrade := s.systemParameterString(ctx, entitlements.ParamBillingUpgradeTokenSecret)
	inbound := s.systemParameterString(ctx, entitlements.ParamBillingInboundSecret)

	if upgrade == "" || inbound == "" || upgrade != inbound {
		return
	}

	slog.ErrorContext(ctx, "SaaS: SECURITY: "+entitlements.ParamBillingUpgradeTokenSecret+
		" is set to the SAME value as "+entitlements.ParamBillingInboundSecret+
		"; the upgrade token is still forgeable by anyone holding the service bearer. "+
		"Generate a distinct secret and set it on both sides "+
		"(SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET here, BILLING_UPGRADE_TOKEN_SECRET on billing)",
		"upgradeTokenParam", entitlements.ParamBillingUpgradeTokenSecret,
		"inboundParam", entitlements.ParamBillingInboundSecret)
}

// systemParameterString reads a string-valued system parameter, returning ""
// when it is absent, unreadable or not a string.
func (s *Server) systemParameterString(ctx context.Context, key string) string {
	param, err := s.dbService.GetSystemParameter(ctx, key)
	if err != nil || param == nil {
		return ""
	}

	value, _ := param.Value["value"].(string)

	return value
}

// seedSigningKeys wires the two request-signing key sets and the legacy-bearer
// escape hatch. Each key set is validated before it is stored, so a malformed
// JSON array surfaces at boot rather than as a stream of 401s later.
//
// The legacy flag is intentionally NOT defaulted here: absent the env var, the
// parameter stays unset and the readers default it to true. Turning it off is
// step 3 of the cross-repo migration and must be a deliberate operator action.
func (s *Server) seedSigningKeys(ctx context.Context) error {
	keySets := []struct {
		env      string
		param    string
		describe string
	}{
		{
			envEntitlementsServiceSigningKeys, entitlements.ParamServiceSigningKeys,
			"inbound (verify the billing entitlements push)",
		},
		{
			envEntitlementsOutboundSigningKeys, entitlements.ParamOutboundSigningKeys,
			"outbound (sign our calls to billing)",
		},
	}

	for i := range keySets {
		keySet := &keySets[i]

		raw := os.Getenv(keySet.env)
		if raw == "" {
			continue
		}

		parsed, err := servicesig.ParseKeySet(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", keySet.env, err)
		}

		if err := s.dbService.SetSystemParameter(ctx, keySet.param, raw, true); err != nil {
			return err
		}

		slog.InfoContext(ctx, "SaaS: seeded entitlements signing keys",
			"key", keySet.param, "direction", keySet.describe, "keyCount", len(parsed))
	}

	if raw := os.Getenv(envEntitlementsAllowLegacyToken); raw != "" {
		allowed, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			slog.WarnContext(ctx, "SaaS: ignoring unparsable "+envEntitlementsAllowLegacyToken,
				"value", raw, "error", parseErr)

			return nil
		}

		if err := s.dbService.SetSystemParameter(
			ctx, entitlements.ParamAllowLegacyServiceToken, allowed, false,
		); err != nil {
			return err
		}

		slog.InfoContext(ctx, "SaaS: legacy entitlements service token acceptance",
			"key", entitlements.ParamAllowLegacyServiceToken, "allowed", allowed)
	}

	return nil
}
