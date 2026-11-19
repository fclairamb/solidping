package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
)

// keyContactOrgName is the template field naming the org of a contact email.
const keyContactOrgName = "OrgName"

// ErrContactCodeNotSent is returned when an email contact's verification code
// could not be queued (no job service, or an invalid address).
var ErrContactCodeNotSent = errors.New("the verification code could not be sent")

// orgNameAndNotificationsURL resolves the org's display name and the page where
// the member manages their notification methods in it.
func (s *Service) orgNameAndNotificationsURL(ctx context.Context, orgUID string) (string, string) {
	dashboardURL := strings.TrimRight(s.fullCfg.Server.BaseURL, "/") + config.DashboardBasePath
	notificationsURL := dashboardURL + "/"
	orgName := ""

	if orgUID != "" {
		if org, err := s.db.GetOrganization(ctx, orgUID); err == nil && org != nil {
			notificationsURL = fmt.Sprintf("%s/orgs/%s/account/notifications", dashboardURL, org.Slug)
			orgName = org.Name
			if orgName == "" {
				orgName = org.Slug
			}
		}
	}

	return orgName, notificationsURL
}

// NotifyContactAdded emails the account owner that a notification method was
// added to their account in an org (spec 2026-11-19-02). A contact receives
// the org's incident pages from then on, so one added behind the owner's back
// must not go unnoticed: the same reasoning as the Discord and Telegram link
// notices. Best effort: the contact is already stored.
func (s *Service) NotifyContactAdded(ctx context.Context, user *models.User, orgUID, display string) {
	if user == nil {
		return
	}

	orgName, notificationsURL := s.orgNameAndNotificationsURL(ctx, orgUID)

	s.enqueueEmail(ctx, "", user.Email, email.TemplateContactAdded, recipient.ForUser(user),
		map[string]any{
			"AddedAt":           time.Now().UTC(),
			"ContactValue":      display,
			keyContactOrgName:   orgName,
			keyNotificationsURL: notificationsURL,
		},
	)
}

// SendContactVerificationCode emails the code that confirms an email contact
// (spec 2026-11-19-02) to the contact's address, which may be anyone's: the
// mail names the requesting account so a stranger knows what to ignore.
func (s *Service) SendContactVerificationCode(
	ctx context.Context, user *models.User, orgUID, address, code string,
) error {
	if user == nil {
		return ErrContactCodeNotSent
	}

	orgName, _ := s.orgNameAndNotificationsURL(ctx, orgUID)

	if !s.enqueueEmail(ctx, "", address, email.TemplateContactVerifyCode, recipient.ForUser(user),
		map[string]any{
			"Code":            code,
			"AccountEmail":    user.Email,
			keyContactOrgName: orgName,
		},
	) {
		return ErrContactCodeNotSent
	}

	return nil
}
