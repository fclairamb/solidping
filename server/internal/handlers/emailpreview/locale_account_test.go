package emailpreview

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// Render tests of the account mail (registration, welcome, password-*,
// email-changed, invitation, membership_*) in every language, from the same
// fixtures the preview serves.

// accountTemplates is every template of the account group.
//
//nolint:gochecknoglobals // read-only test table.
var accountTemplates = []string{
	"registration.html", "welcome.html", "password-reset.html", "password-reset-sso.html",
	"password-changed.html", "email-changed.html", "device-token-created.html", "passkey-added.html", "totp-enabled.html",
	"invitation.html",
	"membership-request-new.html", "membership-request-decision.html",
}

const nbsp = " "

func renderAccount(t *testing.T, tmpl string, loc i18n.Locale, data any) (string, string, string) {
	t.Helper()

	formatter, err := email.NewFormatter()
	require.NoError(t, err)

	subject, html, text, err := formatter.Format(tmpl, loc, data)
	require.NoError(t, err, tmpl)

	return subject, html, text
}

func accountFixture(t *testing.T, tmpl string, loc i18n.Locale) map[string]any {
	t.Helper()

	data, ok := fixtureFor(tmpl, loc)
	require.True(t, ok, tmpl)

	return data
}

// requireNoMissingAccountKeys fails on any account.* key a render looked up
// and did not find (other namespaces belong to other groups' tests).
func requireNoMissingAccountKeys(t *testing.T) {
	t.Helper()

	for _, missing := range i18n.MissingKeys() {
		_, key, _ := strings.Cut(missing, ":")
		require.False(t, strings.HasPrefix(key, "account."), "missing catalog key %s", missing)
	}
}

// TestAccountMail_EveryLocale checks a translated subject and a translated
// sentence of the plaintext part of each template, in each language. English
// is pinned too: it must read as it did before the catalogs.
func TestAccountMail_EveryLocale(t *testing.T) {
	t.Parallel()

	type want struct{ subject, text string }

	cases := map[string]map[string]want{
		"registration.html": {
			i18n.LangEnglish: {"Confirm your SolidPing account", "This confirmation link expires in 3 days."},
			i18n.LangFrench: {
				"Confirmez votre compte SolidPing",
				"Ce lien de confirmation expire dans 3" + nbsp + "jours.",
			},
			i18n.LangGerman: {
				"Bestätigen Sie Ihr SolidPing-Konto",
				"Dieser Bestätigungslink läuft in 3" + nbsp + "Tagen ab.",
			},
			i18n.LangSpanish: {
				"Confirma tu cuenta de SolidPing",
				"Este enlace de confirmación caduca en 3" + nbsp + "días.",
			},
		},
		"welcome.html": {
			i18n.LangEnglish: {"Welcome to SolidPing", "Open your dashboard: " + fixtureDashboardURL},
			i18n.LangFrench:  {"Bienvenue sur SolidPing", "Ouvrir mon tableau de bord : " + fixtureDashboardURL},
			i18n.LangGerman:  {"Willkommen bei SolidPing", "Dashboard öffnen: " + fixtureDashboardURL},
			i18n.LangSpanish: {"Bienvenido a SolidPing", "Abrir tu panel: " + fixtureDashboardURL},
		},
		"password-reset.html": {
			i18n.LangEnglish: {"Reset your SolidPing password", "This reset link expires in 1 hour."},
			i18n.LangFrench: {
				"Réinitialisez votre mot de passe SolidPing",
				"Ce lien de réinitialisation expire dans 1" + nbsp + "heure.",
			},
			i18n.LangGerman: {
				"Setzen Sie Ihr SolidPing-Passwort zurück",
				"Dieser Link läuft in 1" + nbsp + "Stunde ab.",
			},
			i18n.LangSpanish: {
				"Restablece tu contraseña de SolidPing",
				"Este enlace de restablecimiento caduca en 1" + nbsp + "hora.",
			},
		},
		"password-reset-sso.html": {
			i18n.LangEnglish: {
				"Reset your SolidPing password",
				"You usually sign in to SolidPing with GitHub or Google.",
			},
			i18n.LangFrench: {
				"Réinitialisez votre mot de passe SolidPing",
				"Vous vous connectez habituellement à SolidPing avec GitHub ou Google.",
			},
			i18n.LangGerman: {
				"Setzen Sie Ihr SolidPing-Passwort zurück",
				"Normalerweise melden Sie sich bei SolidPing mit GitHub oder Google an.",
			},
			i18n.LangSpanish: {
				"Restablece tu contraseña de SolidPing",
				"Normalmente inicias sesión en SolidPing con GitHub o Google.",
			},
		},
		"password-changed.html": {
			i18n.LangEnglish: {
				"Your SolidPing password was changed",
				"your SolidPing password was changed on Jul 5, 10:00 UTC.",
			},
			i18n.LangFrench:  {"Votre mot de passe SolidPing a été modifié", "a été modifié le 5 juil., 10:00 UTC."},
			i18n.LangGerman:  {"Ihr SolidPing-Passwort wurde geändert", "am 5. Juli, 10:00 UTC geändert wurde."},
			i18n.LangSpanish: {"Se ha cambiado tu contraseña de SolidPing", "se cambió el 5 jul, 10:00 UTC."},
		},
		"totp-enabled.html": {
			i18n.LangEnglish: {
				"Two-factor authentication was turned on for your SolidPing account",
				"was turned on for your SolidPing account on Jul 5, 10:00 UTC.",
			},
			i18n.LangFrench: {
				"La double authentification a été activée sur votre compte SolidPing",
				"a été activée sur votre compte SolidPing le 5 juil., 10:00 UTC.",
			},
			i18n.LangGerman: {
				"Für Ihr SolidPing-Konto wurde die Zwei-Faktor-Authentifizierung aktiviert",
				"am 5. Juli, 10:00 UTC die Zwei-Faktor-Authentifizierung aktiviert",
			},
			i18n.LangSpanish: {
				"Se ha activado la verificación en dos pasos en tu cuenta de SolidPing",
				"El 5 jul, 10:00 UTC se activó la verificación en dos pasos",
			},
		},
		"passkey-added.html": {
			i18n.LangEnglish: {
				"A passkey was added to your SolidPing account",
				"It signs in to every organization of the account.",
			},
			i18n.LangFrench: {
				"Une clé d'accès a été ajoutée à votre compte SolidPing",
				"Une clé d'accès nommée « iCloud Keychain » a été ajoutée",
			},
			i18n.LangGerman: {
				"Ihrem SolidPing-Konto wurde ein Passkey hinzugefügt",
				"ein Passkey mit dem Namen „iCloud Keychain“ hinzugefügt",
			},
			i18n.LangSpanish: {
				"Se ha añadido una llave de acceso a tu cuenta de SolidPing",
				"una llave de acceso llamada «iCloud Keychain»",
			},
		},
		"device-token-created.html": {
			i18n.LangEnglish: {
				"A device was signed in to your SolidPing account",
				"The request was opened from IP address 203.0.113.7.",
			},
			i18n.LangFrench: {
				"Un appareil s'est connecté à votre compte SolidPing", //nolint:misspell // French
				"La demande a été ouverte depuis l'adresse IP 203.0.113.7.",
			},
			i18n.LangGerman: {
				"Ein Gerät wurde bei Ihrem SolidPing-Konto angemeldet",
				"Die Anfrage wurde von der IP-Adresse 203.0.113.7 gestellt.",
			},
			i18n.LangSpanish: {
				"Se ha conectado un dispositivo a tu cuenta de SolidPing",
				"La solicitud se abrió desde la dirección IP 203.0.113.7.",
			},
		},
		"email-changed.html": {
			i18n.LangEnglish: {
				"Your SolidPing email address was changed",
				"changed from alice@acme.com to alice@acme.org on Jul 5, 10:00 UTC.",
			},
			i18n.LangFrench: {
				"Votre adresse e-mail SolidPing a été modifiée",
				"remplacée par alice@acme.org le 5 juil., 10:00 UTC. L'ancienne adresse était alice@acme.com.",
			},
			i18n.LangGerman: {
				"Ihre SolidPing-E-Mail-Adresse wurde geändert",
				"am 5. Juli, 10:00 UTC von alice@acme.com in alice@acme.org geändert.",
			},
			i18n.LangSpanish: {
				"Se ha cambiado tu correo de SolidPing",
				"se cambió de alice@acme.com a alice@acme.org el 5 jul, 10:00 UTC.",
			},
		},
		"invitation.html": {
			i18n.LangEnglish: {
				"You're invited to join Acme Corp on SolidPing",
				"Alice Admin has invited you to join Acme Corp on SolidPing as admin.",
			},
			i18n.LangFrench: {
				"Invitation à rejoindre Acme Corp sur SolidPing",
				"Alice Admin vous invite à rejoindre Acme Corp sur SolidPing en tant qu'administrateur.",
			},
			i18n.LangGerman: {
				"Einladung zu Acme Corp bei SolidPing",
				"Alice Admin hat Sie eingeladen, Acme Corp bei SolidPing als Admin beizutreten.",
			},
			i18n.LangSpanish: {
				"Te han invitado a unirte a Acme Corp en SolidPing",
				"Alice Admin te ha invitado a unirte a Acme Corp en SolidPing como administrador.",
			},
		},
		"membership-request-new.html": {
			i18n.LangEnglish: {
				"New membership request from Bob Builder on Acme Corp",
				"\"I'd like to help monitor our new services.\"",
			},
			i18n.LangFrench: {
				"Nouvelle demande d'adhésion à Acme Corp : Bob Builder",
				"« I'd like to help monitor our new services. »",
			},
			i18n.LangGerman: {
				"Neue Mitgliedschaftsanfrage von Bob Builder für Acme Corp",
				"„I'd like to help monitor our new services.“",
			},
			i18n.LangSpanish: {
				"Nueva solicitud de adhesión de Bob Builder a Acme Corp",
				"«I'd like to help monitor our new services.»",
			},
		},
		"membership-request-decision.html": {
			i18n.LangEnglish: {"Your request to join Acme Corp was approved", "You're now a member with role viewer."},
			i18n.LangFrench: {
				"Votre demande d'adhésion à Acme Corp a été approuvée",
				"Vous êtes désormais membre avec le rôle lecteur.",
			},
			i18n.LangGerman: {
				"Ihre Anfrage zum Beitritt zu Acme Corp wurde genehmigt",
				"Sie sind jetzt Mitglied mit der Rolle Betrachter.",
			},
			i18n.LangSpanish: {
				"Se ha aprobado tu solicitud para unirte a Acme Corp",
				"Ahora eres miembro con el rol lector.",
			},
		},
	}

	require.Len(t, cases, len(accountTemplates), "every account template has expectations")

	for _, tmpl := range accountTemplates {
		for _, lang := range i18n.Supported() {
			t.Run(tmpl+"/"+lang, func(t *testing.T) {
				t.Parallel()
				r := require.New(t)

				loc := i18n.New(lang, "")
				subject, html, text := renderAccount(t, tmpl, loc, accountFixture(t, tmpl, loc))

				r.Equal(cases[tmpl][lang].subject, subject)
				r.Contains(text, cases[tmpl][lang].text)
				r.Contains(html, `<html lang="`+lang+`">`)
				r.NotContains(html+text, "—", "no em dash in the copy")
				requireNoMissingAccountKeys(t)
			})
		}
	}
}

// TestAccountMail_DatesInTheReaderZone: the security notices show the change
// in the reader's timezone, with its abbreviation.
func TestAccountMail_DatesInTheReaderZone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	data := map[string]any{"ChangedAt": fixtureChangedAt(), "OldEmail": "alice@acme.com", "NewEmail": "alice@acme.org"}

	_, html, text := renderAccount(t, "password-changed.html", i18n.New(i18n.LangFrench, "Europe/Paris"), data)
	r.Contains(text, "modifié le 5 juil., 12:00 CEST.")
	r.Contains(html, "Modification effectuée le 5 juil., 12:00 CEST.")

	_, _, text = renderAccount(t, "email-changed.html", i18n.New(i18n.LangSpanish, "America/Mexico_City"), data)
	r.Contains(text, "el 5 jul, 04:00 CST.")
}

// TestAccountMail_EveryRoleAndDecision: the role and the decision pick whole
// translated sentences, so every role must have them in every language.
func TestAccountMail_EveryRoleAndDecision(t *testing.T) {
	t.Parallel()

	// invited is how the invitation names the role, approved how the approval
	// does.
	type roleWords struct{ invited, approved string }

	roles := map[string]map[string]roleWords{
		"admin": {
			i18n.LangEnglish: {"as admin", "with role admin."},
			i18n.LangFrench:  {"qu'administrateur", "avec le rôle administrateur."},
			i18n.LangGerman:  {"als Admin", "mit der Rolle Admin."},
			i18n.LangSpanish: {"como administrador", "con el rol administrador."},
		},
		"user": {
			i18n.LangEnglish: {"as user", "with role user."},
			i18n.LangFrench:  {"qu'utilisateur", "avec le rôle utilisateur."},
			i18n.LangGerman:  {"als Benutzer", "mit der Rolle Benutzer."},
			i18n.LangSpanish: {"como usuario", "con el rol usuario."},
		},
		"viewer": {
			i18n.LangEnglish: {"as viewer", "with role viewer."},
			i18n.LangFrench:  {"que lecteur", "avec le rôle lecteur."},
			i18n.LangGerman:  {"als Betrachter", "mit der Rolle Betrachter."},
			i18n.LangSpanish: {"como lector", "con el rol lector."},
		},
	}

	rejectedSubjects := map[string]string{
		i18n.LangEnglish: "Your request to join " + fixtureOrgName + " was rejected",
		i18n.LangFrench:  "Votre demande d'adhésion à " + fixtureOrgName + " a été rejetée",
		i18n.LangGerman:  "Ihre Anfrage zum Beitritt zu " + fixtureOrgName + " wurde abgelehnt",
		i18n.LangSpanish: "Se ha rechazado tu solicitud para unirte a " + fixtureOrgName,
	}

	for role, words := range roles {
		for _, lang := range i18n.Supported() {
			t.Run(role+"/"+lang, func(t *testing.T) {
				t.Parallel()
				r := require.New(t)

				loc := i18n.New(lang, "")

				invite := accountFixture(t, "invitation.html", loc)
				invite["Role"] = role
				_, _, withInviter := renderAccount(t, "invitation.html", loc, invite)
				r.Contains(withInviter, fixturePersonName)
				r.Contains(withInviter, words[lang].invited)

				invite["InviterName"] = ""
				_, _, anonymous := renderAccount(t, "invitation.html", loc, invite)
				r.NotContains(anonymous, fixturePersonName)
				r.Contains(anonymous, words[lang].invited)

				decision := accountFixture(t, "membership-request-decision.html", loc)
				decision["Role"] = role
				approvedSubject, _, approvedText := renderAccount(t, "membership-request-decision.html", loc, decision)
				r.Contains(approvedText, words[lang].approved)

				decision["Decision"] = "rejected"
				decision["Reason"] = "Not this quarter"
				rejectedSubject, _, rejectedText := renderAccount(t, "membership-request-decision.html", loc, decision)
				r.Equal(rejectedSubjects[lang], rejectedSubject)
				r.NotEqual(approvedSubject, rejectedSubject)
				r.Contains(rejectedText, "Not this quarter")
				r.NotContains(rejectedText, words[lang].approved, "a rejection names no role")

				requireNoMissingAccountKeys(t)
			})
		}
	}
}

// TestAccountMail_JSONRoundTrip renders each fixture as the email job does,
// after its JSON round trip (times become RFC 3339 strings), and expects the
// same mail as a direct render.
func TestAccountMail_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	for _, tmpl := range accountTemplates {
		for _, lang := range i18n.Supported() {
			t.Run(tmpl+"/"+lang, func(t *testing.T) {
				t.Parallel()
				r := require.New(t)

				loc := i18n.New(lang, "Europe/Paris")
				data := accountFixture(t, tmpl, loc)

				raw, err := json.Marshal(data)
				r.NoError(err)

				var decoded any
				r.NoError(json.Unmarshal(raw, &decoded))

				subject, html, text := renderAccount(t, tmpl, loc, data)
				jobSubject, jobHTML, jobText := renderAccount(t, tmpl, loc, decoded)

				r.Equal(subject, jobSubject)
				r.Equal(html, jobHTML)
				r.Equal(text, jobText)
			})
		}
	}
}
