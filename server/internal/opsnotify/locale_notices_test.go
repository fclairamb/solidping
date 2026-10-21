package opsnotify_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
)

// The English text of every notice is what Slack, Telegram, Discord, push and
// SMS have always carried. These are the strings the code built before the
// notices were localized, byte for byte.
func TestNoticesEnglishTextIsUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	support := opsnotify.NewSupportMessageNotice(&opsnotify.SupportMessage{
		NewThread: true, Channel: "sms", From: "+33600000000", Preview: "is the api down?", Folded: 19,
	}, "https://solidping.example/d/support/thread-1")
	r.Equal("[SolidPing support] New support thread on sms from +33600000000", support.Subject)
	r.Equal("Channel: sms\nFrom:    +33600000000\n\nis the api down?\n\n"+
		"…and 19 more message(s) in this thread since the last notification.\n\n"+
		"Replying to this notification does NOT reach the sender — open the thread to answer them.", support.Body)
	r.Equal(opsnotify.EventSupportMessage, support.Event)
	r.Equal("https://solidping.example/d/support/thread-1", support.URL)

	reply := opsnotify.NewSupportMessageNotice(&opsnotify.SupportMessage{
		Channel: "telegram", From: "@bob", Preview: "hello",
	}, "")
	r.Equal("[SolidPing support] New message on telegram from @bob", reply.Subject)
	r.Equal("Channel: telegram\nFrom:    @bob\n\nhello\n\n"+
		"Replying to this notification does NOT reach the sender — open the thread to answer them.", reply.Body)

	signup := opsnotify.NewUserRegisteredNotice(&opsnotify.UserRegistered{
		Email: "carol@acme.com", Name: "Carol", Method: "password",
	}, "user-1")
	r.Equal("[SolidPing] New signup: carol@acme.com (password)", signup.Subject)
	r.Equal("A new user just signed up on this SolidPing instance.\n\n"+
		"Email:  carol@acme.com\nName:   Carol\nMethod: password", signup.Body)
	r.Equal("user-1", signup.AboutUserUID)

	signup.SetLandingOrgs([]string{"acme", "beta"})
	r.True(strings.HasSuffix(signup.Body, "\nMethod: password\nOrg:    acme, beta"), signup.Body)

	invited := opsnotify.NewUserRegisteredNotice(&opsnotify.UserRegistered{
		Email: "erin@acme.com", Method: "invite",
	}, "user-3")
	invited.SetLandingOrgs([]string{"acme"})
	r.Equal("[SolidPing] New signup: erin@acme.com (invite)", invited.Subject)
	r.Equal("A new user just signed up on this SolidPing instance.\n\n"+
		"Email:  erin@acme.com\nMethod: invite\nOrg:    acme", invited.Body)

	nameless := opsnotify.NewUserRegisteredNotice(&opsnotify.UserRegistered{
		Email: "dave@acme.com", Method: "google",
	}, "user-2")
	nameless.SetLandingOrgs(nil)
	r.Equal("A new user just signed up on this SolidPing instance.\n\n"+
		"Email:  dave@acme.com\nMethod: google\nOrg:    no organization", nameless.Body)

	test := opsnotify.NewTestNotice("alice@acme.com", "https://solidping.example/d/")
	r.Equal("[SolidPing] Operator notification test", test.Subject)
	r.Equal("This is a test of the operator notifications on this SolidPing instance.\n"+
		"If you are reading it, the route it arrived on works.\n\n"+
		"Requested by alice@acme.com.", test.Body)
}

// localizedNotices is one notice of every kind built in this package, the
// landing organization already resolved.
func localizedNotices() map[string]*opsnotify.Notice {
	signup := opsnotify.NewUserRegisteredNotice(&opsnotify.UserRegistered{
		Email: "carol@acme.com", Name: "Carol", Method: "password",
	}, "user-1")
	signup.SetLandingOrgs([]string{"acme"})

	orphan := opsnotify.NewUserRegisteredNotice(&opsnotify.UserRegistered{
		Email: "dave@acme.com", Method: "google",
	}, "user-2")
	orphan.SetLandingOrgs(nil)

	invited := opsnotify.NewUserRegisteredNotice(&opsnotify.UserRegistered{
		Email: "erin@acme.com", Method: "invite",
	}, "user-3")
	invited.SetLandingOrgs([]string{"acme", "beta"})

	return map[string]*opsnotify.Notice{
		"support": opsnotify.NewSupportMessageNotice(&opsnotify.SupportMessage{
			NewThread: true, Channel: "sms", From: "+33600000000", Preview: "is the api down?", Folded: 1,
		}, "https://solidping.example/d/support/thread-1"),
		"supportFolded": opsnotify.NewSupportMessageNotice(&opsnotify.SupportMessage{
			Channel: "sms", From: "+33600000000", Preview: "still down", Folded: 3,
		}, ""),
		"signup":  signup,
		"orphan":  orphan,
		"invited": invited,
		"test":    opsnotify.NewTestNotice("alice@acme.com", "https://solidping.example/d/"),
	}
}

// Every notice written in every language: translated, every key found, and
// the English writing identical to the text every other medium sends.
func TestNoticesAreWrittenInEveryLanguage(t *testing.T) {
	t.Parallel()

	expected := map[string]map[string][]string{
		"fr": {
			"support": {
				"[SolidPing support] Nouveau fil de support de +33600000000 sur sms",
				"Expéditeur : +33600000000", "…et 1 autre message dans ce fil", "ouvrez le fil",
			},
			"supportFolded": {
				"[SolidPing support] Nouveau message de +33600000000 sur sms", "…et 3 autres messages dans ce fil",
			},
			"signup": {
				"[SolidPing] Nouvelle inscription : carol@acme.com (mot de passe)", "Nom : Carol",
				"Méthode : mot de passe", "Organisation : acme", //nolint:misspell // French
			},
			"orphan": {"Organisation : aucune", "Méthode : google"}, //nolint:misspell // French
			"invited": {
				"[SolidPing] Nouvelle inscription : erin@acme.com (invitation)", "Méthode : invitation",
				"Organisations : acme, beta", //nolint:misspell // French
			},
			"test": {"[SolidPing] Test des notifications opérateur", "Demandé par alice@acme.com."},
		},
		"de": {
			"support": {
				"[SolidPing support] Neue Support-Konversation von +33600000000 via sms", //nolint:misspell // German
				"…und 1 weitere Nachricht",
			},
			"supportFolded": {"Neue Nachricht von +33600000000 via sms", "…und 3 weitere Nachrichten"},
			"signup": {
				"[SolidPing] Neue Registrierung: carol@acme.com (Passwort)", "Methode: Passwort",
				"Organisation: acme", //nolint:misspell // German
			},
			"orphan": {"Organisation: keine"}, //nolint:misspell // German
			"invited": {
				"[SolidPing] Neue Registrierung: erin@acme.com (Einladung)",
				"Organisationen: acme, beta",
			},
			"test": {
				"Test der Betreiber-Benachrichtigungen", "Angefordert von alice@acme.com.",
				"über den sie zugestellt wurde.",
			},
		},
		"es": {
			"support": {
				"[SolidPing support] Nuevo hilo de soporte de +33600000000 en sms",
				"…y 1 mensaje más en este hilo",
			},
			"supportFolded": {"Nuevo mensaje de +33600000000 en sms", "…y 3 mensajes más"},
			"signup": {
				"[SolidPing] Nuevo registro: carol@acme.com (contraseña)", "Método: contraseña", "Organización: acme",
			},
			"orphan": {"Organización: ninguna"},
			"invited": {
				"[SolidPing] Nuevo registro: erin@acme.com (invitación)", "Organizaciones: acme, beta",
			},
			"test": {"Prueba de las notificaciones para el operador", "Solicitada por alice@acme.com."},
		},
	}

	for name, notice := range localizedNotices() {
		r := require.New(t)

		subject, body := notice.Localized.Localize(i18n.English)
		r.Equal(notice.Subject, subject, name)
		r.Equal(notice.Body, body, name)

		for lang, byNotice := range expected {
			subject, body := notice.Localized.Localize(i18n.New(lang, "Europe/Paris"))
			text := subject + "\n" + body

			r.NotEqual(notice.Body, body, "%s/%s is still English", name, lang)
			r.NotContains(text, "notices.", "%s/%s shows a catalog key", name, lang)
			r.NotContains(text, "—", "%s/%s carries an em dash", name, lang)

			for _, want := range byNotice[name] {
				r.Contains(text, want, "%s/%s", name, lang)
			}
		}
	}

	for _, missing := range i18n.MissingKeys() {
		require.NotContains(t, missing, ":notices.", "a notices key is missing from a catalog")
	}
}

// emailCapture is a Deps whose email medium records the locale it was handed.
type emailCapture struct {
	subject, text, lang, timezone string
}

func (e *env) depsCapturingEmail(capture *emailCapture) opsnotify.Deps {
	deps := e.deps()
	deps.EnqueueEmail = func(_ context.Context, orgUID, to string, loc i18n.Locale, subject, text string) error {
		capture.subject, capture.text = subject, text
		capture.lang, capture.timezone = loc.Lang(), loc.Timezone()

		return e.record("email", orgUID, to, subject, text)
	}

	return deps
}

// TestDeliverWritesTheEmailInTheRecipientsLanguage: the email follows the
// super admin's own preferences; every other medium keeps the English text,
// byte for byte.
func TestDeliverWritesTheEmailInTheRecipientsLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newEnv(t)
	ctx := t.Context()

	org := env.org(t, "acme")
	operator := env.user(t, "alice@acme.com", true)
	env.join(t, org, operator)
	r.NoError(env.db.UpdateUser(ctx, operator.UID, &models.UserUpdate{
		Preferences: &models.UserPreferences{Locale: "fr", Timezone: "Europe/Paris"},
	}))

	env.contact(t, org, operator, models.UserContactTypeEmail, "alice@acme.com", false)
	env.contact(t, org, operator, models.UserContactTypeSlackUser, "U1", false)
	env.contact(t, org, operator, models.UserContactTypeDiscord, "D1", false)
	env.contact(t, org, operator, models.UserContactTypeTelegram, "42", false)
	env.contact(t, org, operator, models.UserContactTypeWebPush, `{"endpoint":"x"}`, false)
	env.contact(t, org, operator, models.UserContactTypePhone, "+33600000001", true)

	notice := localizedNotices()["support"]
	capture := &emailCapture{}

	report := opsnotify.DeliverToUser(ctx, env.depsCapturingEmail(capture), env.log, operator.UID, notice)
	r.Equal(6, report.Delivered)

	r.Equal("fr", capture.lang)
	r.Equal("Europe/Paris", capture.timezone)
	r.Equal("[SolidPing support] Nouveau fil de support de +33600000000 sur sms", capture.subject)
	r.Contains(capture.text, "Canal : sms")
	r.True(strings.HasSuffix(capture.text, "\n\nhttps://solidping.example/d/support/thread-1"), capture.text)

	englishText := notice.Body + "\n\n" + notice.URL

	for _, delivery := range env.deliveries() {
		switch delivery.medium {
		case "email":
			continue
		case "slack", "discord":
			r.Equal(englishText, delivery.body, delivery.medium)
		case "telegram":
			r.Contains(delivery.body, "<b>"+notice.Subject+"</b>")
			r.Contains(delivery.body, "Replying to this notification does NOT reach the sender")
		case "webpush":
			r.Equal(notice.Subject, delivery.subject)
			r.Equal(opsnotify.ShortBody(notice)+"|"+notice.URL, delivery.body)
		case "sms":
			r.Equal(opsnotify.SMSBody(notice), delivery.body)
		}

		r.NotContains(delivery.body, "Canal", "%s must stay English", delivery.medium)
	}
}

// TestDeliverEmailsAnUnstructuredNoticeAsIs: a notice queued before the
// upgrade has no structured form. Its email is the English text, as it always
// was, even to a French reader.
func TestDeliverEmailsAnUnstructuredNoticeAsIs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newEnv(t)
	ctx := t.Context()

	org := env.org(t, "acme")
	operator := env.user(t, "alice@acme.com", true)
	env.join(t, org, operator)
	r.NoError(env.db.UpdateUser(ctx, operator.UID, &models.UserUpdate{
		Preferences: &models.UserPreferences{Locale: "fr"},
	}))
	env.contact(t, org, operator, models.UserContactTypeEmail, "alice@acme.com", false)

	notice := testNotice()
	capture := &emailCapture{}

	report := opsnotify.DeliverToUser(ctx, env.depsCapturingEmail(capture), env.log, operator.UID, notice)
	r.Equal(1, report.Delivered)
	r.Equal(notice.Subject, capture.subject)
	r.Equal(notice.Body+"\n\n"+notice.URL, capture.text)
	r.Equal("en", capture.lang, "the job carries the language the text is written in")
}

// TestDeliverEmailsAnEnglishReaderTheSameText: with no preference, the email
// is English and UTC, and reads exactly like the other media.
func TestDeliverEmailsAnEnglishReaderTheSameText(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newEnv(t)

	org := env.org(t, "acme")
	operator := env.user(t, "alice@acme.com", true)
	env.join(t, org, operator)
	env.contact(t, org, operator, models.UserContactTypeEmail, "alice@acme.com", false)

	notice := localizedNotices()["signup"]
	capture := &emailCapture{}

	opsnotify.DeliverToUser(t.Context(), env.depsCapturingEmail(capture), env.log, operator.UID, notice)
	r.Equal("en", capture.lang)
	r.Equal("UTC", capture.timezone)
	r.Equal(notice.Subject, capture.subject)
	r.Equal(notice.Body, capture.text)
}
