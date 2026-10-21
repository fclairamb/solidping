package emailpreview_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/emailpreview"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// commonNamespace is the catalog namespace phase A localizes everywhere
// (base.html and the shared partials). The other namespaces arrive with phase
// B; until then a template may still render English text of its own, which
// is not a failure yet (spec 2026-10-21-01, phase C tightens this).
const commonNamespace = "common."

// TestPreview_EveryTemplateRendersInEveryLocale renders every shipped template
// in every supported language, through the same route the dashboard uses:
// HTML, plaintext and the index subject. None may carry template syntax or a
// missing value, and no key of the common namespace may be missing from any
// catalog.
func TestPreview_EveryTemplateRendersInEveryLocale(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)

	for _, lang := range i18n.Supported() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			index := doGet(t, router, "/api/mgmt/email-preview?locale="+lang)
			r.Equal(http.StatusOK, index.Code, index.Body.String())

			var payload struct {
				Data []emailpreview.TemplateSummary `json:"data"`
			}
			r.NoError(json.Unmarshal(index.Body.Bytes(), &payload))

			subjects := map[string]string{}
			for _, row := range payload.Data {
				subjects[row.Template] = row.Subject
			}

			for _, tmpl := range shippedTemplates(t) {
				subject := subjects[tmpl]
				r.NotEmpty(subject, "%s/%s: no subject", lang, tmpl)
				r.NotContains(subject, "<no value>", "%s/%s subject", lang, tmpl)
				r.NotContains(subject, "{{", "%s/%s subject", lang, tmpl)

				base := "/api/mgmt/email-preview/" + tmpl + "?locale=" + lang

				html := doGet(t, router, base)
				r.Equal(http.StatusOK, html.Code, "%s/%s html: %s", lang, tmpl, html.Body.String())
				r.NotContains(html.Body.String(), "{{", "%s/%s html", lang, tmpl)
				r.Contains(html.Body.String(), `<html lang="`+lang+`">`, "%s/%s html", lang, tmpl)

				text := doGet(t, router, base+"&format=text")
				r.Equal(http.StatusOK, text.Code, "%s/%s text", lang, tmpl)
				r.NotContains(text.Body.String(), "{{", "%s/%s text", lang, tmpl)
				r.NotContains(text.Body.String(), "<no value>", "%s/%s text", lang, tmpl)
			}
		})
	}

	// Runs after the parallel renders above (t.Cleanup of the parent waits for
	// its subtests), so every render's lookups are recorded by then.
	t.Cleanup(func() {
		for _, missing := range missingCommonKeys(i18n.MissingKeys()) {
			t.Errorf("catalog key missing while rendering: %s", missing)
		}
	})
}

// missingCommonKeys keeps the "<lang>:<key>" entries of the common namespace.
func missingCommonKeys(all []string) []string {
	var out []string

	for _, missing := range all {
		if _, key, _ := strings.Cut(missing, ":"); strings.HasPrefix(key, commonNamespace) {
			out = append(out, missing)
		}
	}

	return out
}

// TestMissingCommonKeys_PositiveControl proves the absence check above can
// fail: a missing lookup is recorded, and the filter keeps common keys only.
func TestMissingCommonKeys_PositiveControl(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	i18n.New(i18n.LangGerman, "").T("emailpreview.control.key")
	r.Contains(i18n.MissingKeys(), "de:emailpreview.control.key")

	r.Equal([]string{"fr:common.gone"}, missingCommonKeys([]string{"fr:common.gone", "de:emailpreview.control.key"}))
}

// TestPreview_LocaleDefaultsToEnglish is the positive control of the locale
// parameter: no parameter and locale=en are the same bytes, and another
// language is not (so the comparison cannot pass because the parameter is
// simply ignored). Only the HTML part is compared with French: until phase B
// localizes the template bodies, subjects and text parts are still English.
func TestPreview_LocaleDefaultsToEnglish(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router := newTestRouter(t)

	for _, path := range []string{
		"/api/mgmt/email-preview/incident-created.html",
		"/api/mgmt/email-preview/incident-created.html?format=text",
		"/api/mgmt/email-preview/registration.html",
		"/api/mgmt/email-preview",
	} {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}

		plain := doGet(t, router, path)
		english := doGet(t, router, path+sep+"locale=en")

		r.Equal(http.StatusOK, plain.Code, path)
		r.Equal(plain.Body.String(), english.Body.String(), path)

		if !strings.Contains(path, "format=text") && strings.HasSuffix(path, ".html") {
			french := doGet(t, router, path+sep+"locale=fr")
			r.NotEqual(plain.Body.String(), french.Body.String(), path)
		}
	}

	html := doGet(t, router, "/api/mgmt/email-preview/incident-created.html").Body.String()
	r.Contains(html, `<html lang="en">`)
	r.Contains(html, "I&#39;m on it")
	r.Contains(html, "The link works until Jul 12, 10:00 UTC.")
}

// TestPreview_FrenchUsesTheCommonCatalog spot-checks what phase A localizes:
// the shared chrome of base.html and its partials.
func TestPreview_FrenchUsesTheCommonCatalog(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router := newTestRouter(t)

	incident := doGet(t, router, "/api/mgmt/email-preview/incident-created.html?locale=fr").Body.String()
	r.Contains(incident, `<html lang="fr">`)
	r.Contains(incident, "Je m&#39;en occupe")
	r.Contains(incident, "Voir l&#39;incident")
	r.Contains(incident, "Le lien est valable jusqu&#39;au 12 juil., 10:00 UTC.")
	r.Contains(incident, "Ne plus recevoir les alertes pour Production API")
	r.NotContains(incident, "The link works")

	registration := doGet(t, router, "/api/mgmt/email-preview/registration.html?locale=fr").Body.String()
	r.Contains(registration, "Le bouton ne fonctionne pas ? Copiez-collez ce lien dans votre navigateur :")
	r.NotContains(registration, "Button not working")
}

func TestPreview_InvalidLocale400s(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)

	for _, path := range []string{
		"/api/mgmt/email-preview/welcome.html?locale=it",
		"/api/mgmt/email-preview/welcome.html?locale=fr-FR",
		"/api/mgmt/email-preview/welcome.html?locale=FR",
		"/api/mgmt/email-preview?locale=xx",
	} {
		rec := doGet(t, router, path)
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.Contains(t, rec.Body.String(), "VALIDATION_ERROR", path)
	}
}
