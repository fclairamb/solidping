package jobtypes

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
)

// TestEscalationEmailViewModelLetsTheRecipientAck pins what the escalation mail
// gives the person it wakes up: an "I'm on it" magic link signed for them, the
// recorded cause, and the probed target.
func TestEscalationEmailViewModelLetsTheRecipientAck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")

	checkName := "API health"
	check := &models.Check{
		UID: "check-with-url", OrganizationUID: env.org.UID, Name: &checkName, Type: "http",
		Config: models.JSONMap{"url": "https://api.acme.com/health"},
	}
	r.NoError(env.db.CreateCheck(ctx, check))

	incident := *env.incident
	incident.CheckUID = check.UID
	// Recent, so the 7-day token is still valid.
	incident.StartedAt = time.Now().UTC().Add(-time.Hour).Truncate(time.Minute).Add(30 * time.Second)
	incident.Details = models.JSONMap{"failure_reason": "HTTP 503 Service Unavailable (expected 200)"}

	viewModel := newRun().buildEscalationEmailViewModel(ctx, env.jctx, &incident, "oncall@acme.com", i18n.English)

	r.Equal("API health", viewModel["CheckName"])
	r.Equal("HTTP 503 Service Unavailable (expected 200)", viewModel["FailureReason"])
	r.Equal("https://api.acme.com/health", viewModel["CheckTarget"])
	r.Equal(incident.StartedAt, viewModel["StartedAt"], "the template formats it in the reader's zone")

	ackURL, ok := viewModel["AckURL"].(string)
	r.True(ok)
	r.True(strings.HasPrefix(ackURL,
		"https://app.example.com/api/v1/orgs/"+env.org.Slug+"/incidents/"+incident.UID+"/ack?token="), ackURL)

	parsed, err := url.Parse(ackURL)
	r.NoError(err)

	payload, err := incidentlinks.Verify([]byte("test-secret"), incident.UID, parsed.Query().Get("token"))
	r.NoError(err, "the token is signed for this incident with the app secret")
	r.Equal("oncall@acme.com", payload.RecipientEmail, "the link acks as the person it was sent to")
	r.Equal(incident.StartedAt.Add(incidentlinks.AckTokenTTL).Unix(), payload.Expiry.Unix())

	// Without app config there is no secret to sign with: no button, no crash.
	noConfig := *env.jctx
	noConfig.AppConfig = nil
	r.Empty(newRun().buildEscalationEmailViewModel(ctx, &noConfig, &incident, "oncall@acme.com", i18n.English)["AckURL"])
}

// TestDispatch_EscalationEmailThreadsWithTheIncident: the escalation mail joins
// the conversation of the other mails about the same incident, and offers the
// "I'm on it" button.
func TestDispatch_EscalationEmailThreadsWithTheIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupPhoneEnv(t, false, "")

	sent := newRun().dispatchRoute(context.Background(), env.jctx, slog.Default(), env.incident, emailRoute(),
		map[string]bool{"email": true})
	r.Equal(1, sent)

	msg := env.emails.lastMessage()
	r.NotNil(msg)
	r.Equal("incident."+env.incident.UID, msg.ThreadID)
	r.Contains(msg.HTML, "I&#39;m on it")
	r.Contains(msg.HTML, "/incidents/"+env.incident.UID+"/ack?token=")
	r.Contains(msg.Text, "I'm on it: ")
	r.Equal(i18n.LangEnglish, msg.Locale, "no account and no owner preference: English")
	r.Equal("UTC", msg.Timezone)
}

// seedEscalationUser creates an account with the given preferences.
func seedEscalationUser(t *testing.T, env *phoneTestEnv, address, locale, timezone string) *models.User {
	t.Helper()

	user := models.NewUser(address)
	user.Preferences = &models.UserPreferences{Locale: locale, Timezone: timezone}
	require.NoError(t, env.db.CreateUser(t.Context(), user))

	return user
}

func emailRouteFor(user *models.User, address string) *models.UserNotificationRoute {
	route := emailRoute()
	route.UserUID = user.UID
	route.Contact.UserUID = user.UID
	route.Contact.Value = address

	return route
}

// TestEscalationEmail_ContactSpeaksItsReadersLanguage: a mail to one of a
// user's email contacts reads the account behind that address when there is
// one, else the user who owns the contact. Dates follow the reader's zone.
func TestEscalationEmail_ContactSpeaksItsReadersLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupPhoneEnv(t, false, "")
	owner := seedEscalationUser(t, env, "lea@acme.com", "fr", "Europe/Paris")
	seedEscalationUser(t, env, "team@acme.com", "de", "Europe/Berlin")

	sent := newRun().dispatchRoute(t.Context(), env.jctx, slog.Default(), env.incident,
		emailRouteFor(owner, "pager@acme.com"), map[string]bool{"email": true})
	r.Equal(1, sent)

	msg := env.emails.lastMessage()
	r.Equal("🟠 API health est toujours en panne, incident escaladé vers vous (#1)", msg.Subject)
	r.Equal("fr", msg.Locale)
	r.Equal("Europe/Paris", msg.Timezone)
	r.Contains(msg.Text, "Personne n'a acquitté l'incident à temps, il vous a donc été escaladé.")
	r.Contains(msg.Text, "Je m'en occupe : ")

	paris, err := time.LoadLocation("Europe/Paris")
	r.NoError(err)
	r.Contains(msg.Text, "Depuis : "+i18n.New("fr", "Europe/Paris").Date(env.incident.StartedAt))
	r.Contains(msg.Text, env.incident.StartedAt.In(paris).Format("15:04 MST"))

	// The address belongs to another account: its own preferences win.
	newRun().dispatchRoute(t.Context(), env.jctx, slog.Default(), env.incident,
		emailRouteFor(owner, "TEAM@acme.com"), map[string]bool{"email": true})

	msg = env.emails.lastMessage()
	r.Equal("🟠 API health ist weiterhin ausgefallen, an Sie eskaliert (#1)", msg.Subject)
	r.Equal("de", msg.Locale)
	r.Equal("Europe/Berlin", msg.Timezone)
}

// TestEscalationEmail_UserTargetSpeaksTheirLanguage: a user target with no
// route is emailed at their account address, in their own language.
func TestEscalationEmail_UserTargetSpeaksTheirLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupPhoneEnv(t, false, "")
	user := seedEscalationUser(t, env, "ana@acme.com", "es", "Europe/Madrid")

	r.Equal(1, newRun().pageUser(t.Context(), env.jctx, slog.Default(), env.incident, &user.UID, nil))

	msg := env.emails.lastMessage()
	r.Equal([]string{"ana@acme.com"}, msg.Recipients.To)
	r.Equal("🟠 API health sigue fallando, se te ha escalado el incidente (#1)", msg.Subject)
	r.Equal("es", msg.Locale)
	r.Equal("Europe/Madrid", msg.Timezone)
	r.Contains(msg.HTML, "Nadie ha reconocido")
}

// TestEscalationEmail_UnknownCheckIsLocalized: a check deleted since the
// incident opened is named in the reader's language; the other channels keep
// the English constant.
func TestEscalationEmail_UnknownCheckIsLocalized(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupPhoneEnv(t, false, "")

	incident := *env.incident
	incident.CheckUID = "gone"

	fr := newRun().buildEscalationEmailViewModel(t.Context(), env.jctx, &incident, "a@acme.com", i18n.New("fr", ""))
	r.Equal("Contrôle inconnu", fr["CheckName"])

	en := newRun().buildEscalationEmailViewModel(t.Context(), env.jctx, &incident, "a@acme.com", i18n.English)
	r.Equal(escalationUnknownCheckName, en["CheckName"])
	r.Equal(escalationUnknownCheckName, escalationCheckName(nil))
}

// disabledPhoneRoute gives user a phone route that is switched off, so a page
// walks its routes, delivers nothing and falls back to the direct email.
func disabledPhoneRoute(t *testing.T, env *phoneTestEnv, user *models.User) {
	t.Helper()

	contact := models.NewUserContact(user.UID, env.org.UID, models.UserContactTypePhone, "+33600000000", "mobile")
	require.NoError(t, env.db.UpsertUserContact(t.Context(), contact))

	route := models.NewUserNotificationRoute(user.UID, env.org.UID, contact.UID, 0)
	route.Enabled = false
	_, err := env.db.DB().NewInsert().Model(route).Exec(t.Context())
	require.NoError(t, err)
}

// TestEscalationEmail_OnCallUserSpeaksTheirLanguage: the on-call user of a
// schedule target is emailed in their own language and zone, both when they
// have no route and when none of their routes delivered.
func TestEscalationEmail_OnCallUserSpeaksTheirLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupPhoneEnv(t, false, "")

	noRoute := seedEscalationUser(t, env, "jonas@acme.com", "de", "Europe/Berlin")
	noRouteSchedule := "schedule-email-locale-no-route"
	registerOnCall(t, noRouteSchedule, noRoute)

	r.Equal(1, newRun().pageSchedule(t.Context(), env.jctx, slog.Default(), env.incident, &noRouteSchedule, nil))

	msg := env.emails.lastMessage()
	r.Equal([]string{"jonas@acme.com"}, msg.Recipients.To)
	r.Equal("🟠 API health ist weiterhin ausgefallen, an Sie eskaliert (#1)", msg.Subject)
	r.Equal("de", msg.Locale)
	r.Equal("Europe/Berlin", msg.Timezone)

	routedOff := seedEscalationUser(t, env, "lea@acme.com", "fr", "Europe/Paris")
	disabledPhoneRoute(t, env, routedOff)

	routedOffSchedule := "schedule-email-locale-routes-off"
	registerOnCall(t, routedOffSchedule, routedOff)

	r.Equal(1, newRun().pageSchedule(t.Context(), env.jctx, slog.Default(), env.incident, &routedOffSchedule, nil))

	msg = env.emails.lastMessage()
	r.Equal([]string{"lea@acme.com"}, msg.Recipients.To)
	r.Equal("🟠 API health est toujours en panne, incident escaladé vers vous (#1)", msg.Subject)
	r.Equal("fr", msg.Locale)
	r.Equal("Europe/Paris", msg.Timezone)
}

// TestEscalationEmail_AdminFallbackSpeaksTheirLanguage: an all-admins target
// emails each admin with no delivering route in that admin's language and
// zone.
func TestEscalationEmail_AdminFallbackSpeaksTheirLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupPhoneEnv(t, false, "")

	admin := seedEscalationUser(t, env, "ana@acme.com", "es", "Europe/Madrid")
	r.NoError(env.db.CreateOrganizationMember(t.Context(),
		models.NewOrganizationMember(env.org.UID, admin.UID, models.MemberRoleAdmin)))

	r.Equal(1, newRun().pageAllAdmins(t.Context(), env.jctx, slog.Default(), env.incident, nil))

	msg := env.emails.lastMessage()
	r.Equal([]string{"ana@acme.com"}, msg.Recipients.To)
	r.Equal("🟠 API health sigue fallando, se te ha escalado el incidente (#1)", msg.Subject)
	r.Equal("es", msg.Locale)
	r.Equal("Europe/Madrid", msg.Timezone)

	// Same admin, a route that is switched off: still the fallback, still Spanish.
	disabledPhoneRoute(t, env, admin)
	r.Equal(1, newRun().pageAllAdmins(t.Context(), env.jctx, slog.Default(), env.incident, nil))
	r.Equal("es", env.emails.lastMessage().Locale)
}
