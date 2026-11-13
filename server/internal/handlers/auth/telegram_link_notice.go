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

// keyNotificationsURL is the template field of the Telegram link notice
// pointing at the page where the contact can be removed.
const keyNotificationsURL = "NotificationsURL"

// NotifyTelegramLinked emails the account owner that a Telegram account was
// linked to their SolidPing account (spec 2026-11-13-03). With Sign in with
// Telegram on, the linked chat signs in, and survives a password change and the
// revocation of every session, so one linked behind the owner's back must not
// go unnoticed. Best effort, like notifyDiscordLinked: the link is already
// stored.
//
// It runs from the Telegram webhook, which carries no session claims, so the
// org the connect link was minted in names the notifications page.
func (s *Service) NotifyTelegramLinked(ctx context.Context, userUID, orgUID string) {
	user, err := s.db.GetUser(ctx, userUID)
	if err != nil || user == nil {
		slog.WarnContext(ctx, "Telegram link notice: user not found", "userUid", userUID, "error", err)

		return
	}

	dashboardURL := strings.TrimRight(s.fullCfg.Server.BaseURL, "/") + config.DashboardBasePath
	notificationsURL := dashboardURL + "/"

	if orgUID != "" {
		if org, orgErr := s.db.GetOrganization(ctx, orgUID); orgErr == nil && org != nil {
			notificationsURL = fmt.Sprintf("%s/orgs/%s/account/notifications", dashboardURL, org.Slug)
		}
	}

	s.enqueueEmail(ctx, "", user.Email, email.TemplateTelegramLinked, recipient.ForUser(user),
		map[string]any{
			"LinkedAt":          time.Now().UTC(),
			keyNotificationsURL: notificationsURL,
		},
	)
}
