package auth

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
)

// notifyDiscordLinked emails the account owner that a Discord account was
// linked to their SolidPing account (spec 2026-11-01-03). A linked Discord
// identity signs in, and survives a password change and the revocation of every
// session, so one linked behind the owner's back must not go unnoticed. Best
// effort, like notifyPasskeyAdded: the link is already stored.
//
// It runs from the public OAuth callback, which carries no session claims, so
// the org of the page that started the link names the security page.
func (s *Service) notifyDiscordLinked(ctx context.Context, userUID, orgUID string) {
	user, err := s.db.GetUser(ctx, userUID)
	if err != nil || user == nil {
		slog.WarnContext(ctx, "Discord link notice: user not found", "userUid", userUID, "error", err)

		return
	}

	dashboardURL := strings.TrimRight(s.fullCfg.Server.BaseURL, "/") + config.DashboardBasePath
	securityURL := dashboardURL + "/"

	if orgUID != "" {
		if org, orgErr := s.db.GetOrganization(ctx, orgUID); orgErr == nil && org != nil {
			securityURL = fmt.Sprintf("%s/orgs/%s/account/security", dashboardURL, org.Slug)
		}
	}

	s.enqueueEmail(ctx, "", user.Email, email.TemplateDiscordLinked, recipient.ForUser(user),
		map[string]any{
			"LinkedAt":    time.Now().UTC(),
			"SecurityURL": securityURL,
		},
	)
}
