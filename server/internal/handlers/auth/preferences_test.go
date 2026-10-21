package auth

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

func TestMergePreferences(t *testing.T) {
	t.Parallel()

	stored := &models.UserPreferences{Locale: "fr", Timezone: "Europe/Paris"}

	cases := []struct {
		name    string
		current *models.UserPreferences
		req     PreferencesRequest
		want    *models.UserPreferences
		wantErr error
	}{
		{
			"nothing stored, locale set", nil,
			PreferencesRequest{Locale: ptr("de")},
			&models.UserPreferences{Locale: "de"}, nil,
		},
		{
			"a full tag is stored normalized", nil,
			PreferencesRequest{Locale: ptr("es-419")},
			&models.UserPreferences{Locale: "es"}, nil,
		},
		{
			"timezone only keeps the locale", stored,
			PreferencesRequest{Timezone: ptr("America/New_York")},
			&models.UserPreferences{Locale: "fr", Timezone: "America/New_York"}, nil,
		},
		{
			"empty string clears", stored,
			PreferencesRequest{Locale: ptr("")},
			&models.UserPreferences{Timezone: "Europe/Paris"}, nil,
		},
		{
			"both cleared", stored,
			PreferencesRequest{Locale: ptr(" "), Timezone: ptr("")},
			&models.UserPreferences{}, nil,
		},
		{"unsupported locale", stored, PreferencesRequest{Locale: ptr("it")}, nil, ErrUnsupportedLocale},
		{"unknown timezone", stored, PreferencesRequest{Timezone: ptr("Europe/Atlantis")}, nil, ErrInvalidTimezone},
		{"the server's Local zone", stored, PreferencesRequest{Timezone: ptr("Local")}, nil, ErrInvalidTimezone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got, err := mergePreferences(tc.current, &tc.req)
			if tc.wantErr != nil {
				r.ErrorIs(err, tc.wantErr)

				return
			}

			r.NoError(err)
			r.Equal(tc.want, got)
		})
	}

	t.Run("the stored value is not mutated", func(t *testing.T) {
		t.Parallel()

		_, err := mergePreferences(stored, &PreferencesRequest{Locale: ptr("de")})
		require.NoError(t, err)
		require.Equal(t, "fr", stored.Locale)
	})
}

func TestSignupPreferences(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(&models.UserPreferences{Locale: "fr", Timezone: "Europe/Paris"},
		signupPreferences("fr-FR", "Europe/Paris"))
	r.Equal(&models.UserPreferences{Timezone: "Asia/Tokyo"}, signupPreferences("ja-JP", "Asia/Tokyo"),
		"an unsupported language is dropped, the zone kept")
	r.Equal(&models.UserPreferences{Locale: "de"}, signupPreferences("de", "Not/A_Zone"))
	r.Nil(signupPreferences("", ""))
	r.Nil(signupPreferences("pt", "Local"))

	r.Equal(&models.UserPreferences{Locale: "es"},
		signupPreferencesFromState(map[string]any{"locale": "es", "timezone": "nope"}))
	r.Nil(signupPreferencesFromState(nil))
	r.Nil(signupPreferencesFromState("garbage"))
}

func decodeMe(t *testing.T, body []byte) *MeResponse {
	t.Helper()

	var me MeResponse
	require.NoError(t, json.Unmarshal(body, &me))

	return &me
}

// TestUpdateMePreferences covers PATCH /auth/me with preferences: alone (the
// early return on a nil name used to swallow it), partial, cleared, and
// refused with 400 VALIDATION_ERROR before anything is written. The steps
// share one user and run in order.
func TestUpdateMePreferences(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, ctx := newEmailChangeFixture(t, "prefs@acme.com")

	stored := func() *models.User {
		t.Helper()

		user, err := f.db.GetUser(ctx, f.user.UID)
		r.NoError(err)

		return user
	}

	// /auth/me reports an empty object before anything is set.
	me, err := f.svc.GetUserInfo(ctx, f.claimsA)
	r.NoError(err)
	r.Equal(&models.UserPreferences{}, me.User.Preferences)

	raw, err := json.Marshal(me.User)
	r.NoError(err)
	r.Contains(string(raw), `"preferences":{}`)

	// A preferences-only patch is applied, normalized.
	rec := patchMe(t, ctx, f.handler, f.claimsA, `{"preferences":{"locale":"fr-FR","timezone":"Europe/Paris"}}`)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Equal(&models.UserPreferences{Locale: "fr", Timezone: "Europe/Paris"},
		decodeMe(t, rec.Body.Bytes()).User.Preferences)
	r.Equal(&models.UserPreferences{Locale: "fr", Timezone: "Europe/Paris"}, stored().Preferences)

	// One field at a time: the other is kept, the name applied alongside.
	rec = patchMe(t, ctx, f.handler, f.claimsA, `{"name":"Prefs","preferences":{"timezone":"Asia/Tokyo"}}`)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Equal("Prefs", decodeMe(t, rec.Body.Bytes()).User.Name)
	r.Equal(&models.UserPreferences{Locale: "fr", Timezone: "Asia/Tokyo"}, stored().Preferences)

	// A bad value is a 400 and writes nothing, not even the name.
	for _, body := range []string{
		`{"name":"Not applied","preferences":{"locale":"it"}}`,
		`{"name":"Not applied","preferences":{"timezone":"Mars/Olympus"}}`,
	} {
		rec = patchMe(t, ctx, f.handler, f.claimsA, body)
		r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())
		r.Contains(rec.Body.String(), string(base.ErrorCodeValidationError))
		r.Equal("Prefs", stored().Name)
		r.Equal(&models.UserPreferences{Locale: "fr", Timezone: "Asia/Tokyo"}, stored().Preferences)
	}

	// Empty strings clear, and NULL is stored.
	rec = patchMe(t, ctx, f.handler, f.claimsA, `{"preferences":{"locale":"","timezone":""}}`)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Equal(&models.UserPreferences{}, decodeMe(t, rec.Body.Bytes()).User.Preferences)
	r.Nil(stored().Preferences)

	// Login responses carry them too (they share newUserInfo).
	rec = patchMe(t, ctx, f.handler, f.claimsA, `{"preferences":{"locale":"de"}}`)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	login, err := f.svc.Login(ctx, emailChangeOrg, "prefs@acme.com", emailChangePassword, Context{})
	r.NoError(err)
	r.Equal(&models.UserPreferences{Locale: "de"}, login.User.Preferences)
}

// TestPreferencesPatchIsRefusedForRestrictedSessions: the preferences ride on
// PATCH /auth/me, which impersonation, demo and must-change-password sessions
// can never reach. The dashboard must not try (spec 2026-10-21-01, decision 5).
func TestPreferencesPatchIsRefusedForRestrictedSessions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.False(IsImpersonationAllowed(http.MethodPatch, ImpersonationPathMe), "impersonation")
	r.False(IsDemoWriteAllowed(http.MethodPatch, PathMe), "demo")
	r.False(IsPasswordRotationExempt(http.MethodPatch, PathMe), "must change password")

	// Positive control: the same sessions may still read /auth/me.
	r.True(IsImpersonationAllowed(http.MethodGet, ImpersonationPathMe))
	r.True(IsPasswordRotationExempt(http.MethodGet, PathMe))
}

// TestSignupCarriesPreferences walks both account-creating flows: the
// preferences of a password signup ride the pending entry to the user row,
// and an invitation accepted by a newcomer seeds them.
func TestSignupCarriesPreferences(t *testing.T) {
	t.Parallel()

	t.Run("register then confirm", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		f, ctx := newLoginAuditFixture(t)
		f.svc.fullCfg.Auth.RegistrationEmailPattern = ".*"

		_, err := f.svc.Register(ctx, RegisterRequest{
			Email: "lea@acme.com", Password: "supersecret123", Locale: "fr-BE", Timezone: "Europe/Brussels",
		})
		r.NoError(err)

		token := extractRegistrationConfirmToken(t, ctx, f.db, "lea@acme.com")
		r.NotEmpty(token)

		login, err := f.svc.ConfirmRegistration(ctx, token)
		r.NoError(err)
		r.Equal(&models.UserPreferences{Locale: "fr", Timezone: "Europe/Brussels"}, login.User.Preferences)

		user, err := f.db.GetUserByEmail(ctx, "lea@acme.com")
		r.NoError(err)
		r.Equal(&models.UserPreferences{Locale: "fr", Timezone: "Europe/Brussels"}, user.Preferences)

		_, err = f.svc.Register(ctx, RegisterRequest{Email: "plain@acme.com", Password: "supersecret123"})
		r.NoError(err)

		_, err = f.svc.ConfirmRegistration(ctx, extractRegistrationConfirmToken(t, ctx, f.db, "plain@acme.com"))
		r.NoError(err)

		plain, err := f.db.GetUserByEmail(ctx, "plain@acme.com")
		r.NoError(err)
		r.Nil(plain.Preferences, "a signup without preferences stores NULL")
	})

	t.Run("accept invite as a newcomer", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		ctx := t.Context()
		f := newAcceptInviteFixture(t, "nuevo@acme.com")

		_, err := f.svc.AcceptInvite(ctx, AcceptInviteRequest{
			Token: f.token, Name: "Nuevo", Password: "nuevo-secret-123", Locale: "es-MX", Timezone: "America/Mexico_City",
		}, "")
		r.NoError(err)

		user, err := f.db.GetUserByEmail(ctx, "nuevo@acme.com")
		r.NoError(err)
		r.Equal(&models.UserPreferences{Locale: "es", Timezone: "America/Mexico_City"}, user.Preferences)
	})
}
