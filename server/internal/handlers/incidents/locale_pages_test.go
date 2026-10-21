package incidents

import (
	"html"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// ackPageKeys is the (title, heading) catalog key of each outcome.
func ackPageKeys() map[ackPageKind][2]string {
	return map[ackPageKind][2]string{
		ackHTMLSuccess:      {"pages.ack.success.title", "pages.ack.success.title"},
		ackHTMLExpired:      {"pages.ack.expired.title", "pages.ack.expired.heading"},
		ackHTMLInvalid:      {"pages.ack.invalid.title", "pages.ack.invalid.heading"},
		ackHTMLMissingToken: {"pages.ack.missingToken.title", "pages.ack.missingToken.title"},
		ackHTMLError:        {"pages.ack.error.title", "pages.ack.error.title"},
	}
}

// TestAckPage_EveryKindInEveryLocale renders each magic-link outcome in each
// language.
func TestAckPage_EveryKindInEveryLocale(t *testing.T) {
	t.Parallel()

	for _, lang := range i18n.Supported() {
		loc := i18n.New(lang, "")

		for kind, keys := range ackPageKeys() {
			r := require.New(t)
			rec := httptest.NewRecorder()
			writeAckPage(rec, loc, http.StatusOK, kind, "acme", "abc-123")

			body := rec.Body.String()
			r.Contains(rec.Header().Values("Vary"), "Accept-Language")
			r.Contains(body, `<html lang="`+lang+`">`)
			r.Contains(body, "<title>"+html.EscapeString(loc.T(keys[0]))+"</title>", "%s %d", lang, kind)
			r.Contains(body, "<h1>"+html.EscapeString(loc.T(keys[1]))+"</h1>", "%s %d", lang, kind)
			r.NotContains(body, "pages.", "a catalog key leaked into the page")
			// The success page's script has braces of its own: name the placeholders.
			r.NotContains(body, "{count}", "a placeholder was left unfilled")
			r.NotContains(body, "{countdown}", "a placeholder was left unfilled")

			if lang != i18n.LangEnglish {
				r.NotContains(body, "<h1>"+i18n.English.T(keys[1])+"</h1>", "%s %d: heading still English", lang, kind)
			}
		}
	}

	for _, missing := range i18n.MissingKeys() {
		require.NotContains(t, missing, ":pages.", "catalog key missing")
	}
}

// TestAckPage_EnglishUnchanged pins the English pages: writeAckHTML is the
// English writeAckPage, and the copy is today's (em dashes aside).
func TestAckPage_EnglishUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for kind := range ackPageKeys() {
		legacy := httptest.NewRecorder()
		writeAckHTML(legacy, http.StatusOK, kind, "acme", "abc-123")

		english := httptest.NewRecorder()
		writeAckPage(english, i18n.English, http.StatusOK, kind, "acme", "abc-123")

		r.Equal(english.Body.String(), legacy.Body.String())
	}

	_, body := successRedirectFragment(i18n.English, "/d/orgs/acme/incidents/abc-123")
	r.Contains(body, `<p>Thanks, the incident has been acknowledged.</p>`+
		`<p>Redirecting to the incident in <span id="ack-countdown" class="ack-countdown">3</span>…</p>`+
		`<p><a id="ack-now" class="ack-now" href="/d/orgs/acme/incidents/abc-123">Go to the incident now</a></p>`)

	bodies := map[ackPageKind]string{
		ackHTMLExpired: `<p>Magic-link ack tokens are valid for 7 days. ` +
			`Open the dashboard to acknowledge this incident manually.</p>`,
		ackHTMLMissingToken: `<p>This URL is missing the <code>token</code> query parameter. ` +
			`Please use the full link from the original email.</p>`,
		ackHTMLError: `<p>We couldn't acknowledge the incident. Please try again, or open ` +
			`the dashboard to acknowledge manually.</p>`,
	}

	for kind, want := range bodies {
		_, _, _, body, _ := pageContent(i18n.English, kind, "", "")
		r.Equal(want, body)
	}
}

// TestAckPage_FrenchCopy pins the French success page, countdown included.
func TestAckPage_FrenchCopy(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	rec := httptest.NewRecorder()
	writeAckPage(rec, i18n.New("fr", ""), http.StatusOK, ackHTMLSuccess, "acme", "abc-123")

	body := rec.Body.String()
	r.Contains(body, "<title>Incident acquitté</title>")
	r.Contains(body, "<p>Merci, l'incident a bien été acquitté.</p>")
	r.Contains(body,
		`<p>Redirection vers l'incident dans <span id="ack-countdown" class="ack-countdown">3</span>…</p>`)
	r.Contains(body, `href="/d/orgs/acme/incidents/abc-123">Accéder à l'incident sans attendre</a>`)

	expired := httptest.NewRecorder()
	writeAckPage(expired, i18n.New("fr", ""), http.StatusGone, ackHTMLExpired, "acme", "abc-123")
	r.Contains(expired.Body.String(), "<p>Les liens d'acquittement envoyés par e-mail sont valables 7 jours. ")
}

// TestAckPageLocale_Chain: the account the token was sent to, then the
// browser's Accept-Language, then English.
func TestAckPageLocale_Chain(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	hans := models.NewUser("hans@example.com")
	hans.Preferences = &models.UserPreferences{Locale: "de", Timezone: "Europe/Berlin"}
	r.NoError(dbSvc.CreateUser(ctx, hans))

	plain := models.NewUser("plain@example.com")
	r.NoError(dbSvc.CreateUser(ctx, plain))

	handler := &Handler{svc: &Service{db: dbSvc}}

	cases := []struct {
		name, email, acceptLanguage, wantLang, wantZone string
	}{
		{"account beats browser", "hans@example.com", "fr-FR,fr;q=0.9", "de", "Europe/Berlin"},
		{"address case is ignored", "HANS@example.com", "es", "de", "Europe/Berlin"},
		{"account without a language", "plain@example.com", "es-ES", "es", "UTC"},
		{"address without an account", "nobody@example.com", "fr", "fr", "UTC"},
		{"token did not verify", "", "de-AT, fr;q=0.5", "de", "UTC"},
		{"unsupported browser language", "", "it-IT, pt;q=0.8", "en", "UTC"},
		{"no header", "nobody@example.com", "", "en", "UTC"},
	}

	for _, tc := range cases {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/ack", nil)
		if tc.acceptLanguage != "" {
			req.Header.Set("Accept-Language", tc.acceptLanguage)
		}

		loc := handler.ackPageLocale(req, tc.email)
		r.Equal(tc.wantLang, loc.Lang(), tc.name)
		r.Equal(tc.wantZone, loc.Timezone(), tc.name)
	}
}

func TestAckAcceptedLanguage(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"":                        "",
		"fr-CA,fr;q=0.9,en;q=0.8": "fr",
		"de;q=0.4, es;q=0.9":      "es",
		"fr;q=0, de":              "de",
		"it-IT, pt-BR;q=0.8":      "",
		"*":                       "",
	}

	for header, want := range cases {
		require.Equal(t, want, acceptedLanguage(header), "Accept-Language %q", header)
	}
}
