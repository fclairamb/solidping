package slack

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/analytics"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// createUserAndCapture inserts a new user row and records the user_signed_up
// product event (spec 2026-08-02-08).
//
// This is this package's single account-creation chokepoint, mirroring
// auth.createUserAndCapture: Sign-in-with-Slack is a real signup path, so it
// must count like every other provider. A guard test
// (signup_analytics_test.go, backed by internal/analytics/signupguard) fails if
// any other file here calls CreateUser directly and bypasses capture.
//
// Privacy: only the user's UUID and the provider family travel — never the
// email address, display name, avatar URL or Slack user id. The label comes
// from auth.SignupMethodSlack rather than a local literal so the two sites
// cannot drift apart and split the metric.
func createUserAndCapture(ctx context.Context, dbSvc db.Service, user *models.User) error {
	if err := dbSvc.CreateUser(ctx, user); err != nil {
		return err
	}

	analytics.Capture(ctx, analytics.Event{
		Name:       analytics.EventUserSignedUp,
		UserUID:    user.UID,
		Properties: map[string]any{"signupMethod": auth.SignupMethodSlack},
	})

	// The instance-level operator notice, raised through auth's own helper so
	// a Slack signup reads identically to every other one (spec 2026-09-03-01).
	// Unlike the analytics event above it DOES carry the email — see the
	// privacy note on NotifyUserRegistered.
	auth.NotifyUserRegistered(ctx, user, auth.SignupMethodSlack)

	return nil
}
