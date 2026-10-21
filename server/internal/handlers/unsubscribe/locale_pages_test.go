package unsubscribe_test

import (
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
)

func doLocalizedRequest(t *testing.T, router *httpx.Router, path, acceptLanguage string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	if acceptLanguage != "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// requirePage asserts rec is the page whose <title> is titleKey and whose
// body shows headingKey, in lang.
func requirePage(t *testing.T, rec *httptest.ResponseRecorder, lang string, status int, titleKey, headingKey string) {
	t.Helper()

	r := require.New(t)
	loc := i18n.New(lang, "")
	body := rec.Body.String()

	r.Equal(status, rec.Code)
	r.Contains(rec.Header().Values("Vary"), "Accept-Language")
	r.Contains(body, `<html lang="`+lang+`">`)
	r.Contains(body, "<title>"+html.EscapeString(loc.T(titleKey))+"</title>")
	r.Contains(body, "<h1>"+string(loc.THTML(headingKey))+"</h1>")
	r.NotContains(body, "pages.", "a catalog key leaked into the page")
	r.NotRegexp(`\{[A-Za-z0-9_]+\}`, body, "a placeholder was left unfilled")

	if lang != i18n.LangEnglish {
		r.NotContains(body, i18n.English.T(headingKey), "the heading is still English")
	}
}

// TestPages_EveryPageInEveryLocale renders each unsubscribe page in each
// language, picked from Accept-Language (the token's address has no account).
func TestPages_EveryPageInEveryLocale(t *testing.T) {
	t.Parallel()

	for _, lang := range i18n.Supported() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f := newTestFixture(t)
			router := newTestRouter(t, f)
			header := lang + ";q=0.9, it;q=0.5"
			future := time.Now().Add(time.Hour)

			checkToken := f.checkToken("check-"+lang+"@example.com", future)
			orgToken := f.orgWideToken("org-"+lang+"@example.com", future)

			rec := doLocalizedRequest(t, router, "/unsubscribe?token="+checkToken, header)
			requirePage(t, rec, lang, http.StatusOK, "pages.unsubscribe.choice.title", "pages.unsubscribe.choice.title")
			r.Contains(rec.Body.String(),
				string(i18n.New(lang, "").THTML("pages.unsubscribe.choice.checkOnly", "check", "unsub-test-check")))

			rec = doLocalizedRequest(t, router, "/unsubscribe?token="+checkToken+"&scope=check", header)
			requirePage(t, rec, lang, http.StatusOK, "pages.unsubscribe.success.title", "pages.unsubscribe.success.title")
			r.Contains(rec.Body.String(), string(i18n.New(lang, "").THTML("pages.unsubscribe.success.check",
				"email", "check-"+lang+"@example.com", "check", "unsub-test-check")))

			rec = doLocalizedRequest(t, router, "/unsubscribe?token="+orgToken+"&scope=org", header)
			requirePage(t, rec, lang, http.StatusOK, "pages.unsubscribe.success.title", "pages.unsubscribe.success.title")
			r.Contains(rec.Body.String(), string(i18n.New(lang, "").THTML("pages.unsubscribe.success.org",
				"email", "org-"+lang+"@example.com")))

			rows, err := f.svc.ListEmailSuppressions(t.Context(), f.org.UID)
			r.NoError(err)
			r.NotEmpty(rows)

			undo := "/unsubscribe/undo?token=" + orgToken + "&uid=" + suppressionOf(rows, "org-"+lang)
			rec = doLocalizedRequest(t, router, undo, header)
			requirePage(t, rec, lang, http.StatusOK,
				"pages.unsubscribe.resubscribed.title", "pages.unsubscribe.resubscribed.title")

			rec = doLocalizedRequest(t, router, "/unsubscribe", header)
			requirePage(t, rec, lang, http.StatusBadRequest,
				"pages.unsubscribe.missing.title", "pages.unsubscribe.missing.heading")

			rec = doLocalizedRequest(t, router, "/unsubscribe/undo", header)
			requirePage(t, rec, lang, http.StatusBadRequest,
				"pages.unsubscribe.missing.undoTitle", "pages.unsubscribe.missing.heading")

			expired := f.checkToken("expired-"+lang+"@example.com", time.Now().Add(-time.Hour))
			rec = doLocalizedRequest(t, router, "/unsubscribe?token="+expired, header)
			requirePage(t, rec, lang, http.StatusBadRequest,
				"pages.unsubscribe.invalid.title", "pages.unsubscribe.invalid.heading")

			goneOrg := incidentlinks.SignUnsubscribe([]byte(testSecret), "gone-org", "gone-"+lang+"@example.com", "", future)
			rec = doLocalizedRequest(t, router, "/unsubscribe?token="+goneOrg, header)
			requirePage(t, rec, lang, http.StatusNotFound,
				"pages.unsubscribe.orgNotFound.title", "pages.unsubscribe.orgNotFound.title")

			for _, missing := range i18n.MissingKeys() {
				r.NotContains(missing, ":pages.", "catalog key missing")
			}
		})
	}
}

func suppressionOf(rows []*models.EmailSuppression, emailPrefix string) string {
	for _, row := range rows {
		if strings.HasPrefix(row.Email, emailPrefix) {
			return row.UID
		}
	}

	return ""
}

// TestPages_FrenchCopy pins a few French sentences, so a regression to a
// literal or English copy shows.
func TestPages_FrenchCopy(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	router := newTestRouter(t, f)

	token := f.checkToken("alice@example.com", time.Now().Add(time.Hour))

	rec := doLocalizedRequest(t, router, "/unsubscribe?token="+token, "fr-FR,fr;q=0.9")
	r.Contains(rec.Body.String(), "<title>Se désabonner des alertes SolidPing</title>")
	r.Contains(rec.Body.String(), "Se désabonner uniquement des alertes pour unsub-test-check</a>")

	rec = doLocalizedRequest(t, router, "/unsubscribe?token="+token+"&scope=check", "fr-FR,fr;q=0.9")
	r.Contains(rec.Body.String(), "<h1>Désabonnement confirmé</h1>")
	r.Contains(rec.Body.String(),
		"<strong>alice@example.com</strong> ne recevra plus les alertes concernant unsub-test-check.")
	r.Contains(rec.Body.String(), "Vous avez changé d'avis ? <a href=\"/unsubscribe/undo?token=")
	r.Contains(rec.Body.String(), ">Annuler le désabonnement</a>")
}

// TestPages_GermanAndSpanishUndo pins the outcome page's wording in German
// and Spanish: "abmelden" alone also means "sign out" in German.
func TestPages_GermanAndSpanishUndo(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	router := newTestRouter(t, f)

	token := f.checkToken("alice@example.com", time.Now().Add(time.Hour))

	rec := doLocalizedRequest(t, router, "/unsubscribe?token="+token+"&scope=org", "de-DE")
	r.Contains(rec.Body.String(), "<h1>Abmeldung bestätigt</h1>")
	r.Contains(rec.Body.String(), "Haben Sie es sich anders überlegt? <a href=\"/unsubscribe/undo?token=")
	r.Contains(rec.Body.String(), ">Abmeldung rückgängig machen</a>")

	rec = doLocalizedRequest(t, router, "/unsubscribe?token="+token+"&scope=org", "es-ES")
	r.Contains(rec.Body.String(), "<h1>Baja confirmada</h1>")
	r.Contains(rec.Body.String(), ">Deshacer la baja</a>")
}

// TestPages_AccountLocaleBeatsAcceptLanguage: the token's address belongs to
// an account with a language, which wins over the browser's.
func TestPages_AccountLocaleBeatsAcceptLanguage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	router := newTestRouter(t, f)

	user := models.NewUser("hans@example.com")
	user.Preferences = &models.UserPreferences{Locale: "de", Timezone: "Europe/Berlin"}
	r.NoError(f.svc.CreateUser(t.Context(), user))

	// The token carries the address in another case: the lookup ignores it.
	token := f.checkToken("Hans@Example.com", time.Now().Add(time.Hour))

	rec := doLocalizedRequest(t, router, "/unsubscribe?token="+token, "fr-FR,fr;q=0.9")
	requirePage(t, rec, "de", http.StatusOK, "pages.unsubscribe.choice.title", "pages.unsubscribe.choice.title")

	// An account without a language leaves the choice to the browser.
	plain := models.NewUser("plain@example.com")
	plain.Preferences = &models.UserPreferences{Timezone: "Europe/Madrid"}
	r.NoError(f.svc.CreateUser(t.Context(), plain))

	token = f.checkToken("plain@example.com", time.Now().Add(time.Hour))
	rec = doLocalizedRequest(t, router, "/unsubscribe?token="+token, "es-ES")
	requirePage(t, rec, "es", http.StatusOK, "pages.unsubscribe.choice.title", "pages.unsubscribe.choice.title")
}

// TestPages_UnsupportedAcceptLanguageIsEnglish: neither an account nor a
// supported browser language gives English.
func TestPages_UnsupportedAcceptLanguageIsEnglish(t *testing.T) {
	t.Parallel()

	f := newTestFixture(t)
	router := newTestRouter(t, f)

	token := f.checkToken("nobody@example.com", time.Now().Add(time.Hour))

	for _, header := range []string{"it-IT, pt-BR;q=0.8", "*", ""} {
		rec := doLocalizedRequest(t, router, "/unsubscribe?token="+token, header)
		requirePage(t, rec, "en", http.StatusOK, "pages.unsubscribe.choice.title", "pages.unsubscribe.choice.title")
	}

	// Positive control: the same request with a supported language is not English.
	rec := doLocalizedRequest(t, router, "/unsubscribe?token="+token, "de")
	requirePage(t, rec, "de", http.StatusOK, "pages.unsubscribe.choice.title", "pages.unsubscribe.choice.title")
}
