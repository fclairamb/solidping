package usernotifications

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestImpersonationCannotBindAChannel is the second wall behind the
// impersonation denylist (spec 2026-09-29-03): even if the route guard were
// bypassed, an impersonated request can neither start a Discord link (which
// would bind the admin's Discord sign-in to the target) nor connect a Discord
// or Telegram DM as the target's paging contact. Each case has the target's
// own, non-impersonated request as positive control.
func TestImpersonationCannotBindAChannel(t *testing.T) {
	t.Parallel()

	t.Run("discord link", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		ctx, env := setupContactEnv(t, WithServerBaseURL("https://solidping.example"))

		_, err := env.svc.CreateDiscordLink(
			audit.WithImpersonator(ctx, "admin-uid"), env.org.Slug, env.user, "")
		r.ErrorIs(err, ErrImpersonationForbidden)

		resp, err := env.svc.CreateDiscordLink(ctx, env.org.Slug, env.user, "")
		r.NoError(err)
		r.NotEmpty(resp.URL)
	})

	t.Run("discord connect", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		ctx, env := setupContactEnv(t)
		r.NoError(env.db.CreateUserProvider(ctx,
			models.NewUserProvider(env.user.UID, models.ProviderTypeDiscord, "111222333444555666")))

		_, err := env.svc.ConnectDiscord(audit.WithImpersonator(ctx, "admin-uid"), env.org.Slug, env.user)
		r.ErrorIs(err, ErrImpersonationForbidden)

		routes, err := env.db.ListUserContactsWithRoutes(ctx, env.user.UID, env.org.UID)
		r.NoError(err)
		r.Empty(routes, "a refused connect must not leave a contact behind")

		_, err = env.svc.ConnectDiscord(ctx, env.org.Slug, env.user)
		r.NoError(err)
	})

	t.Run("telegram link", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		svc, _, org, user := setupTelegramService(t)

		_, err := svc.CreateTelegramLink(audit.WithImpersonator(t.Context(), "admin-uid"), org.Slug, user)
		r.ErrorIs(err, ErrImpersonationForbidden)

		_, err = svc.CreateTelegramLink(t.Context(), org.Slug, user)
		r.NoError(err)
	})
}
