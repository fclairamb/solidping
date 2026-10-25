package regionsweep_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// operatorSpeaks sets the platform_watchdog recipient's language and zone.
func (e *testEnv) operatorSpeaks(t *testing.T, locale, timezone string) {
	t.Helper()

	operator, err := e.db.GetUserByEmail(t.Context(), operatorEmail)
	require.NoError(t, err)
	require.NoError(t, e.db.UpdateUser(t.Context(), operator.UID, &models.UserUpdate{
		Preferences: &models.UserPreferences{Locale: locale, Timezone: timezone},
	}))
}

// TestRegionNoticeIsMailedInTheOperatorsLanguage: the sweep hands the
// structured notice to the delivery, so the operator's email is written in
// their language and zone, from end to end.
func TestRegionNoticeIsMailedInTheOperatorsLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.worker(t, "w-paris", healthyRegion)
	env.strand(t)
	env.check(t, env.acme, "Pinned API", darkRegion)
	env.admin(t, env.acme, "alice@acme.com")
	env.operator(t, true)
	env.operatorSpeaks(t, "fr", "Europe/Paris")

	r.True(transition(env.sweep(t), darkRegion).OperatorNotified)

	r.Len(env.emails(t, operatorEmail,
		"[SolidPing watchdog] La région lauterbourg est hors ligne",
		"La région « lauterbourg » est HORS LIGNE (aucun worker actif) depuis le ",
		`"locale":"fr"`, `"timezone":"Europe/Paris"`), 1)
	r.Empty(env.emails(t, operatorEmail, "lauterbourg is dark"), "the email is not the English text")
}

// TestStalledRegionNoticeIsMailedInTheOperatorsLanguage: same for a stall.
func TestStalledRegionNoticeIsMailedInTheOperatorsLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.worker(t, "w-"+darkRegion, darkRegion)
	env.checkScheduled(t, env.acme, "Late", time.Now().Add(-10*time.Minute), darkRegion)
	env.operator(t, true)
	env.operatorSpeaks(t, "de", "Europe/Berlin")

	r.True(transition(env.sweep(t), darkRegion).OperatorNotified)

	r.Len(env.emails(t, operatorEmail,
		"[SolidPing watchdog] Region lauterbourg hängt", `"locale":"de"`), 1)
}
