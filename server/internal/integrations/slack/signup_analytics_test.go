package slack

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/analytics/signupguard"
)

// TestSlackSignupGoesThroughTheChokepoint guards this package the same way the
// auth package is guarded (spec 2026-08-02-08). Sign-in-with-Slack creates real
// user accounts, so a CreateUser call here that bypasses createUserAndCapture
// would silently under-count user_signed_up — with nothing failing to show it.
func TestSlackSignupGoesThroughTheChokepoint(t *testing.T) {
	t.Parallel()

	offenders := signupguard.ScanDir(t, ".", "signup_analytics.go")

	require.Emptyf(t, offenders,
		"these account-creation sites bypass createUserAndCapture, so user_signed_up "+
			"will silently under-count for Sign-in-with-Slack: %v", offenders)
}
