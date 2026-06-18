package entitlements_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
)

// TestSetResolveRoundTripsDisplayNameAndEmoji verifies the billing-supplied
// plan identity (display name + emoji) survives a Set → Resolve cycle. The
// emoji is the entitlement field the dashboard renders next to the plan name.
func TestSetResolveRoundTripsDisplayNameAndEmoji(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, org, _ := setup(t)
	ctx := t.Context()

	name := "Team"
	emoji := "🚀"
	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits:       entitlements.Limits{MaxChecks: entitlements.Int(100)},
		Source:       models.EntitlementSourceBilling,
		DisplayName:  &name,
		DisplayEmoji: &emoji,
	}, "service:test", "upgrade to team"))

	resolved, err := svc.Resolve(ctx, org.UID)
	r.NoError(err)
	r.NotNil(resolved.DisplayName)
	r.Equal("Team", *resolved.DisplayName)
	r.NotNil(resolved.DisplayEmoji)
	r.Equal("🚀", *resolved.DisplayEmoji)
	r.NotNil(resolved.Limits.MaxChecks)
	r.Equal(100, *resolved.Limits.MaxChecks)
}
