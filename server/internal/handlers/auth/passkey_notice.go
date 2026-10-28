package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
)

// notifyPasskeyAdded emails the account owner a record of the passkey just
// registered (spec 2026-10-28-01). A passkey signs in to every org of the
// account, counts as MFA and survives a password reset, so one added behind
// the owner's back must not go unnoticed. Best effort, like the device token
// notice: the passkey is already stored.
func (s *Service) notifyPasskeyAdded(ctx context.Context, user *models.User, passkey *models.UserPasskey) {
	dashboardURL := strings.TrimRight(s.fullCfg.Server.BaseURL, "/") + config.DashboardBasePath
	securityURL := dashboardURL + "/"

	if claims, ok := ctx.Value(base.ContextKeyClaims).(*Claims); ok && claims.OrgSlug != "" {
		securityURL = fmt.Sprintf("%s/orgs/%s/account/security", dashboardURL, claims.OrgSlug)
	}

	s.enqueueEmail(ctx, "", user.Email, email.TemplatePasskeyAdded, recipient.ForUser(user),
		map[string]any{
			"PasskeyName": passkey.Name,
			"CreatedAt":   time.Now().UTC(),
			"SecurityURL": securityURL,
		},
	)
}
