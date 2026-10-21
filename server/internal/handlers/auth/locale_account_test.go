package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// Each account mail is queued in its reader's language and timezone, resolved
// at enqueue time (spec 2026-10-21-01, decision 6): the signup form for a
// registration, the account for every mail about it, the inviter for an
// invitation to an address with no account, each admin for a membership
// request.

const (
	jobKeyLocale   = "locale"
	jobKeyTimezone = "timezone"
	zoneParis      = "Europe/Paris"
	zoneBerlin     = "Europe/Berlin"
	zoneMexico     = "America/Mexico_City"
)

func accountMailConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{BaseURL: "https://example.com"},
		Auth: config.AuthConfig{
			JWTSecret:                "test-jwt-secret",
			AccessTokenExpiry:        time.Hour,
			RefreshTokenExpiry:       7 * 24 * time.Hour,
			RegistrationEmailPattern: ".*",
		},
		Email: config.EmailConfig{Enabled: true},
	}
}

// emailJobsTo returns the config of every email job queued for address,
// newest first.
//
//nolint:revive // ctx-second matches the existing helpers in this package
func emailJobsTo(t *testing.T, ctx context.Context, dbSvc db.Service, address string) []models.JSONMap {
	t.Helper()

	jobs, err := dbSvc.ListJobs(ctx, nil, 0)
	require.NoError(t, err)

	var out []models.JSONMap

	for _, job := range jobs {
		if job.Type != string(jobdef.JobTypeEmail) {
			continue
		}

		to, _ := job.Config["to"].([]any)
		if len(to) == 1 && to[0] == address {
			out = append(out, job.Config)
		}
	}

	return out
}

// onlyEmailJobTo is the one email job queued for address.
//
//nolint:revive // ctx-second matches the existing helpers in this package
func onlyEmailJobTo(t *testing.T, ctx context.Context, dbSvc db.Service, address string) models.JSONMap {
	t.Helper()

	jobs := emailJobsTo(t, ctx, dbSvc, address)
	require.Len(t, jobs, 1, "email jobs to %s", address)

	return jobs[0]
}

// userWithPrefs creates a user with a password ("oldpassword") and the given
// preferences (none when both are empty).
//
//nolint:revive // ctx-second matches the existing helpers in this package
func userWithPrefs(
	t *testing.T, ctx context.Context, dbSvc db.Service, address, locale, timezone string,
) *models.User {
	t.Helper()

	user := passwordResetUser(t, ctx, dbSvc, address)
	if locale == "" && timezone == "" {
		return user
	}

	prefs := &models.UserPreferences{Locale: locale, Timezone: timezone}
	require.NoError(t, dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{Preferences: prefs}))

	reloaded, err := dbSvc.GetUser(ctx, user.UID)
	require.NoError(t, err)
	require.Equal(t, prefs, reloaded.Preferences)

	return reloaded
}

func requireJobLocale(t *testing.T, job models.JSONMap, lang, timezone string) {
	t.Helper()

	require.Equal(t, lang, job[jobKeyLocale], "job locale")
	require.Equal(t, timezone, job[jobKeyTimezone], "job timezone")
}

func templateDataOf(t *testing.T, job models.JSONMap) map[string]any {
	t.Helper()

	data, ok := job["templateData"].(map[string]any)
	require.True(t, ok, "templateData must decode as a map, got %T", job["templateData"])

	return data
}

func TestAccountMail_RegistrationUsesTheSignupForm(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, locale, timezone string
		wantLang, wantZone     string
	}{
		{"browser language and zone", "fr-FR", zoneParis, i18n.LangFrench, zoneParis},
		{"nothing sent", "", "", i18n.LangEnglish, "UTC"},
		{"unsupported values fall back", "it", "Mars/Olympus", i18n.LangEnglish, "UTC"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, accountMailConfig())
			address := "signup-" + strings.ReplaceAll(tc.name, " ", "-") + "@example.com"

			_, err := svc.Register(ctx, RegisterRequest{
				Name: "Signup", Email: address, Password: "longenough",
				Locale: tc.locale, Timezone: tc.timezone,
			})
			require.NoError(t, err)

			job := onlyEmailJobTo(t, ctx, dbSvc, address)
			require.Equal(t, "registration.html", job["template"])
			requireJobLocale(t, job, tc.wantLang, tc.wantZone)
		})
	}
}

// TestAccountMail_PasswordMailsUseTheAccount covers the reset request and
// both "password changed" notices (after a reset, after a change).
func TestAccountMail_PasswordMailsUseTheAccount(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, accountMailConfig())
	user := userWithPrefs(t, ctx, dbSvc, "pwd-de@example.com", i18n.LangGerman, zoneBerlin)

	_, err := svc.RequestPasswordReset(ctx, RequestPasswordResetRequest{Email: user.Email}, "")
	r.NoError(err)

	reset := onlyEmailJobTo(t, ctx, dbSvc, user.Email)
	r.Equal("password-reset.html", reset["template"])
	requireJobLocale(t, reset, i18n.LangGerman, zoneBerlin)

	resetURL, _ := templateDataOf(t, reset)["ResetURL"].(string)
	token := resetURL[strings.LastIndex(resetURL, "/")+1:]

	before := time.Now().Add(-time.Second)

	_, err = svc.ResetPassword(ctx, ResetPasswordRequest{Token: token, Password: "brandnewpass"})
	r.NoError(err)

	_, err = svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{
		CurrentPassword: "brandnewpass", NewPassword: "evennewerpass",
	})
	r.NoError(err)

	var changed []models.JSONMap

	for _, job := range emailJobsTo(t, ctx, dbSvc, user.Email) {
		if job["template"] == "password-changed.html" {
			changed = append(changed, job)
		}
	}

	r.Len(changed, 2, "one notice after the reset, one after the change")

	for _, job := range changed {
		requireJobLocale(t, job, i18n.LangGerman, zoneBerlin)

		// A time, not a pre-formatted English string: the template formats it
		// in the reader's language and zone.
		raw, _ := templateDataOf(t, job)[tmplKeyChangedAt].(string)
		changedAt, parseErr := time.Parse(time.RFC3339Nano, raw)
		r.NoError(parseErr, "ChangedAt %q", raw)
		r.WithinRange(changedAt, before, time.Now().Add(time.Second))
	}
}

func TestAccountMail_SSOResetListsProvidersInTheReaderLanguage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, accountMailConfig())
	user := ssoOnlyUserWithProviders(t, ctx, dbSvc, "sso-de@example.com",
		models.ProviderTypeGoogle, models.ProviderTypeGitHub)
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{
		Preferences: &models.UserPreferences{Locale: i18n.LangGerman},
	}))

	_, err := svc.RequestPasswordReset(ctx, RequestPasswordResetRequest{Email: user.Email}, "")
	r.NoError(err)

	job := onlyEmailJobTo(t, ctx, dbSvc, user.Email)
	r.Equal("password-reset-sso.html", job["template"])
	requireJobLocale(t, job, i18n.LangGerman, "UTC")
	r.Equal("GitHub oder Google", templateDataOf(t, job)["Providers"])
}

// TestAccountMail_EmailChangedUsesTheAccount: the notice goes to the OLD
// address, which no longer resolves to the account once the row is updated,
// so the locale must come from the user loaded before the change.
func TestAccountMail_EmailChangedUsesTheAccount(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, accountMailConfig())
	user := userWithPrefs(t, ctx, dbSvc, "old-es@example.com", i18n.LangSpanish, zoneMexico)

	r.NoError(svc.applyEmailChange(ctx, &emailChange{
		user: user, newEmail: "new-es@example.com", changedBy: emailChangedBySelf, actorUID: user.UID,
	}))

	job := onlyEmailJobTo(t, ctx, dbSvc, "old-es@example.com")
	r.Equal("email-changed.html", job["template"])
	requireJobLocale(t, job, i18n.LangSpanish, zoneMexico)

	raw, _ := templateDataOf(t, job)[tmplKeyChangedAt].(string)
	_, err := time.Parse(time.RFC3339Nano, raw)
	r.NoError(err, "ChangedAt %q", raw)
}

func TestAccountMail_InvitationUsesTheInviteeElseTheInviter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, accountMailConfig())

	org := models.NewOrganization("invite-locale", "Invite Locale")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	inviter := userWithPrefs(t, ctx, dbSvc, "inviter-fr@example.com", i18n.LangFrench, zoneParis)
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, inviter.UID, models.MemberRoleAdmin)))

	// An account with a language and no zone: its language, the inviter's zone.
	userWithPrefs(t, ctx, dbSvc, "invitee-es@example.com", i18n.LangSpanish, "")

	cases := []struct {
		address            string
		wantLang, wantZone string
		wantExpiresIn      string
	}{
		{"invitee-es@example.com", i18n.LangSpanish, zoneParis, "24\u00a0horas"},
		{"new-address@example.com", i18n.LangFrench, zoneParis, "24\u00a0heures"},
	}

	for _, tc := range cases {
		_, err := svc.CreateInvitation(ctx, org.Slug, inviter.UID, InviteRequest{
			Email: tc.address, Role: string(models.MemberRoleViewer), ExpiresIn: durationLabel24, App: appNameDash0,
		})
		r.NoError(err)

		job := onlyEmailJobTo(t, ctx, dbSvc, tc.address)
		r.Equal("invitation.html", job["template"])
		requireJobLocale(t, job, tc.wantLang, tc.wantZone)
		r.Equal(tc.wantExpiresIn, templateDataOf(t, job)["ExpiresIn"], tc.address)
	}
}

func TestAccountMail_MembershipRequestUsesEachAdmin(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, accountMailConfig())

	org := models.NewOrganization("membership-locale", "Membership Locale")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	admins := []struct {
		address, lang, zone string
		wantLang, wantZone  string
	}{
		{"admin-fr@example.com", i18n.LangFrench, zoneParis, i18n.LangFrench, zoneParis},
		{"admin-de@example.com", i18n.LangGerman, zoneBerlin, i18n.LangGerman, zoneBerlin},
		// No preference: English/UTC, never the requester's or another admin's.
		{"admin-none@example.com", "", "", i18n.LangEnglish, "UTC"},
	}

	for _, admin := range admins {
		user := userWithPrefs(t, ctx, dbSvc, admin.address, admin.lang, admin.zone)
		r.NoError(dbSvc.CreateOrganizationMember(ctx,
			models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))
	}

	requester := userWithPrefs(t, ctx, dbSvc, "requester-es@example.com", i18n.LangSpanish, zoneMexico)
	message := "let me in"

	svc.notifyAdminsOfMembershipRequest(ctx, org, requester.UID, &message)

	for _, admin := range admins {
		job := onlyEmailJobTo(t, ctx, dbSvc, admin.address)
		r.Equal("membership_request_new.html", job["template"])
		requireJobLocale(t, job, admin.wantLang, admin.wantZone)
	}

	for _, decision := range []string{"approved", "rejected"} {
		svc.notifyRequesterOfDecision(ctx, org, requester.UID, decision, string(models.MemberRoleUser), "")
	}

	decisions := emailJobsTo(t, ctx, dbSvc, requester.Email)
	r.Len(decisions, 2)

	for _, job := range decisions {
		r.Equal("membership_request_decision.html", job["template"])
		requireJobLocale(t, job, i18n.LangSpanish, zoneMexico)
	}
}

// TestInviteExpiresIn keeps the English wording of every accepted code (the
// labels the email always showed) and words it in the reader's language.
func TestInviteExpiresIn(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	english := map[string]string{
		"1h": "1 hour", "6h": "6 hours", "12h": "12 hours",
		durationLabel24: "24 hours", "48h": "48 hours", "1w": "7 days",
	}

	r.Len(english, len(getAllowedInviteExpirations()), "every accepted code has a wording")

	for code, want := range english {
		r.Equal(want, InviteExpiresIn(i18n.English, code), code)
	}

	r.Equal("1\u00a0heure", InviteExpiresIn(i18n.New(i18n.LangFrench, ""), "1h"))
	r.Equal("7\u00a0Tagen", InviteExpiresIn(i18n.New(i18n.LangGerman, ""), "1w"))
	r.Equal("48\u00a0horas", InviteExpiresIn(i18n.New(i18n.LangSpanish, ""), "48h"))
	r.Equal("3d", InviteExpiresIn(i18n.New(i18n.LangFrench, ""), "3d"), "an unknown code is printed as is")
}

// TestAccountMail_TemplateLifetimesMatchTheTTLs: registration.html words its
// link lifetime as 3 days and password-reset*.html as 1 hour. Change the
// templates together with these TTLs.
func TestAccountMail_TemplateLifetimesMatchTheTTLs(t *testing.T) {
	t.Parallel()

	require.Equal(t, 3*24*time.Hour, registrationTTL, "registration.html says 3 days")
	require.Equal(t, time.Hour, passwordResetTTL, "password-reset*.html say 1 hour")
}
