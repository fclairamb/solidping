package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	entitlementsapi "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
)

// smsProbingNotifier turns an entitlements destination-probing flag into the
// aggregate ERROR log and the sms.destination_probing operator notice (spec
// 2026-11-15-01). The individual refusals are only WARNs: this is the
// actionable signal.
func smsProbingNotifier(dbService db.Service) entitlementsapi.SMSProbingNotifier {
	return func(ctx context.Context, alert *entitlementsapi.SMSProbingAlert) {
		probe := &opsnotify.SMSDestinationProbing{
			OrgUID:        alert.OrgUID,
			Refusals:      alert.Refusals,
			WindowMinutes: int(alert.Window / time.Minute),
			Prefixes:      alert.Prefixes,
			BlockMinutes:  int(alert.BlockFor / time.Minute),
		}

		if org, err := dbService.GetOrganization(ctx, alert.OrgUID); err == nil && org != nil {
			probe.OrgSlug = org.Slug
		}

		slog.Default().ErrorContext(ctx,
			"SMS destination probing: repeated SP_SMS_ALLOWED_COUNTRIES refusals from one organization; "+
				"its verification sends are blocked",
			"orgUID", alert.OrgUID, "orgSlug", probe.OrgSlug, "refusals", alert.Refusals,
			"destinationPrefixes", alert.Prefixes, "blockedUntil", alert.BlockedUntil)

		opsnotify.Notify(ctx, opsnotify.NewSMSDestinationProbingNotice(probe))
	}
}
