package usernotifications

import (
	"context"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ContactMailer sends the two emails of the generic contact flow (spec
// 2026-11-19-02): the code that confirms an email contact, sent to that
// address, and the notice that tells the account owner a contact was added.
// *auth.Service implements it.
type ContactMailer interface {
	SendContactVerificationCode(ctx context.Context, user *models.User, orgUID, address, code string) error
	NotifyContactAdded(ctx context.Context, user *models.User, orgUID, display string)
}

// WithContactMailer supplies the contact mailer. Without one, an email contact
// for an address other than the sign-in email cannot be confirmed, and no
// notice is sent when a contact is added.
func WithContactMailer(mailer ContactMailer) Option {
	return func(s *Service) { s.contactMailer = mailer }
}

// emailContactBornVerified reports whether a new email contact is usable at
// once: only when it is the user's own sign-in email, and that email is
// verified. Any other address could be anyone's, so it goes through the code
// round trip first, or the instance would relay a member's pages to a stranger.
func emailContactBornVerified(user *models.User, value string) bool {
	return user != nil && user.EmailVerifiedAt != nil &&
		strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(user.Email))
}

// contactCodeVerifiable reports whether a contact type is confirmed by the
// 6-digit code round trip: phone and WhatsApp, and email since spec
// 2026-11-19-02 (models.VerifiableContactTypes keeps its narrower meaning, the
// types an admin may provision).
func contactCodeVerifiable(contactType string) bool {
	return models.ContactRequiresVerification(contactType) || contactType == models.UserContactTypeEmail
}

// contactDisplay is how the "contact added" notice names a contact: its value,
// except for a web push subscription, whose value is an opaque JSON blob and
// whose label names the browser.
func contactDisplay(contactType, value, label string) string {
	if contactType != models.UserContactTypeWebPush {
		return value
	}

	if label != "" {
		return label
	}

	return "web push"
}

// hasContact reports whether routes already carry a contact of that type and
// value: re-posting an existing contact is idempotent and adds nothing.
func hasContact(routes []*models.UserNotificationRoute, contactType, value string) bool {
	for _, r := range routes {
		if r.Contact != nil && r.Contact.Type == contactType && r.Contact.Value == value {
			return true
		}
	}

	return false
}

// notifyContactAdded tells the owner a contact was added, when a mailer is
// wired. Best effort.
func (s *Service) notifyContactAdded(ctx context.Context, user *models.User, orgUID string, req CreateContactRequest) {
	if s.contactMailer == nil {
		return
	}

	s.contactMailer.NotifyContactAdded(ctx, user, orgUID, contactDisplay(req.Type, req.Value, req.Label))
}
