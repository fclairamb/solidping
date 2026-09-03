package auth

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/analytics"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
)

// Signup method labels attached to the user_signed_up product event. These are
// deliberately low-cardinality, non-identifying strings: the provider family,
// never a tenant name, issuer URL, directory DN or email domain.
//
// SignupMethodSlack is exported because the Sign-in-with-Slack path lives in
// internal/integrations/slack and must use the same label — a second literal
// there could drift and split the metric in two.
const (
	signupMethodPassword  = "password"
	signupMethodInvite    = "invite"
	signupMethodGoogle    = "google"
	signupMethodGitHub    = "github"
	signupMethodGitLab    = "gitlab"
	signupMethodMicrosoft = "microsoft"
	signupMethodDiscord   = "discord"
	SignupMethodSlack     = "slack"
	signupMethodOIDC      = "oidc"
	signupMethodSAML      = "saml"
	signupMethodLDAP      = "ldap"
)

// signupMethodSlack is the package-internal alias, so the local call sites read
// consistently with their siblings.
const signupMethodSlack = SignupMethodSlack

// createUserAndCapture inserts a new user row and records the user_signed_up
// product event (spec 2026-08-02-08).
//
// This is the single chokepoint for account creation in this package, and every
// signup path MUST go through it — the email-confirmation flow, invite
// acceptance, and each of the OAuth/OIDC/SAML/LDAP find-or-create paths.
// Capturing at the individual call sites instead is how the event silently
// under-counts: an SSO-only deployment creates all of its accounts through the
// provider services and would otherwise never emit the event at all.
//
// Privacy: only the user's UUID and the provider family travel. The email
// address, display name, avatar URL, IdP issuer and directory DN never do.
// Analytics is a no-op unless PostHog is configured, and the capture is
// asynchronous, so this can never fail or slow down a signup.
func createUserAndCapture(
	ctx context.Context, dbSvc db.Service, user *models.User, method string,
) error {
	if err := dbSvc.CreateUser(ctx, user); err != nil {
		return err
	}

	analytics.Capture(ctx, analytics.Event{
		Name:       analytics.EventUserSignedUp,
		UserUID:    user.UID,
		Properties: map[string]any{"signupMethod": method},
	})

	NotifyUserRegistered(ctx, user, method)

	return nil
}

// NotifyUserRegistered raises the instance-level `user.registered` operator
// notice (spec 2026-09-03-01).
//
// PRIVACY, deliberately different from the analytics capture above: this
// notice DOES carry the email address and the display name. That is not an
// oversight and must not be "fixed". Its only recipients are super admins,
// who can already read both in the users admin, and an operator who is told
// "somebody signed up" without being told who cannot welcome them or notice
// that the signup got stuck. The analytics event stays UID-only because it
// leaves the instance; this one never does.
//
// Fire-and-forget: opsnotify.Notify cannot fail or block, so a signup can
// never fail because a messaging provider is down.
// Exported because Sign-in-with-Slack creates accounts from
// internal/integrations/slack, which has its own account-creation chokepoint.
// One function, so the two sites cannot drift into two different notices.
func NotifyUserRegistered(ctx context.Context, user *models.User, method string) {
	body := "A new user just signed up on this SolidPing instance.\n\n" +
		"Email:  " + user.Email + "\n"

	if user.Name != "" {
		body += "Name:   " + user.Name + "\n"
	}

	body += "Method: " + method

	opsnotify.Notify(ctx, opsnotify.Notice{
		Event:   opsnotify.EventUserRegistered,
		Subject: "[SolidPing] New signup: " + user.Email + " (" + method + ")",
		Body:    body,
		// The landing organization is resolved at delivery time — it does not
		// exist yet at this point, on any signup path.
		AboutUserUID: user.UID,
	})
}
