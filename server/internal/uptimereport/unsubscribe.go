package uptimereport

import (
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
)

// UnsubscribeTokenTTL is how long a digest's unsubscribe link stays valid.
// Deliberately long: a recipient may act on a report months after receiving it,
// and an expired unsubscribe link is a complaint, not a security control.
const UnsubscribeTokenTTL = 365 * 24 * time.Hour

// UnsubscribeURL mints the per-recipient, ORG-WIDE unsubscribe link for a
// digest.
//
// Org-wide rather than check-scoped on purpose: a monthly digest is not about
// one check, so "stop sending me this" cannot honestly be narrowed to one. The
// resulting suppression also removes the address from every report schedule in
// the org (handlers/unsubscribe), so the operator cannot re-enable it by
// re-saving the schedule.
//
// Returns "" when the deployment has no signing secret or base URL configured —
// the caller then sends no unsubscribe headers at all rather than a link that
// cannot be verified.
func UnsubscribeURL(cfg *config.Config, orgSlug, recipient string, now time.Time) string {
	if cfg == nil || cfg.Auth.JWTSecret == "" || cfg.Server.BaseURL == "" {
		return ""
	}

	token := incidentlinks.SignUnsubscribe(
		[]byte(cfg.Auth.JWTSecret), orgSlug, recipient, "", now.Add(UnsubscribeTokenTTL),
	)

	return fmt.Sprintf("%s/unsubscribe?token=%s", cfg.Server.BaseURL, token)
}
