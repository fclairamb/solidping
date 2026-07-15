package app

import (
	"context"
	"log/slog"
	"os"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/entitlements"
)

// SaaS-mode seed env vars. koanf collapses every underscore in an SP_*
// name into a dot, so these multi-word keys are read manually here
// (same pattern as SP_RUN_MODE / SP_SHUTDOWN_TIMEOUT in config.Load).
const (
	envEntitlementsServiceToken  = "SP_ENTITLEMENTS_SERVICE_TOKEN"
	envEntitlementsUpgradeURL    = "SP_ENTITLEMENTS_UPGRADE_URL_TEMPLATE"
	envEntitlementsAdminWrites   = "SP_ENTITLEMENTS_ADMIN_WRITES_ENABLED"
	envEntitlementsBillingSecret = "SP_ENTITLEMENTS_BILLING_INBOUND_SECRET"
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
