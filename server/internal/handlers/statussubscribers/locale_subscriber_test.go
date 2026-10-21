package statussubscribers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// setPageLanguage stores lang in the status page's language column. nil
// leaves the column as a new page has it: NULL.
func setPageLanguage(t *testing.T, s *subSetup, lang *string) {
	t.Helper()

	if lang == nil {
		return
	}

	require.NoError(t, s.dbSvc.UpdateStatusPage(t.Context(), s.page.UID, &models.StatusPageUpdate{Language: lang}))
}

// notifyOne publishes one update of the given shape to a single confirmed
// subscriber and returns the mail it received.
func notifyOne(t *testing.T, s *subSetup, kind string, incidentUID *string, priorUpdates int) *capturedMail {
	t.Helper()

	sender := &fakeSender{}
	link := "https://status.example.com/s/acme/acme-status"
	statussubscribers.NewNotifier(s.dbSvc, sender, testFormatter(t), "https://status.example.com", nil, nil, nil).
		NotifyStatusUpdate(t.Context(), statussubscribers.UpdateEventFor(
			s.page.UID, incidentUID, kind, "API outage", "We are on it.", &link, "Acme Status", priorUpdates))

	msgs := sender.sent()
	require.Len(t, msgs, 1)

	return &capturedMail{
		subject: msgs[0].Subject, html: msgs[0].HTML, text: msgs[0].Text,
		locale: msgs[0].Locale, timezone: msgs[0].Timezone,
	}
}

type capturedMail struct {
	subject, html, text, locale, timezone string
}

// subscriberCopy is what each language must show, for one kind of update.
type subscriberCopy struct {
	subject, reason, unsubscribeLine, moreInfo string
}

func TestSubscriberUpdateMailFollowsThePageLanguage(t *testing.T) {
	t.Parallel()

	// The banner per kind and language, then the lines every update carries.
	labels := map[string]map[string]string{
		"opened":   {"en": "New incident", "fr": "Nouvel incident", "de": "Neue Störung", "es": "Nueva incidencia"},
		"update":   {"en": "Status update", "fr": "Mise à jour", "de": "Status-Update", "es": "Actualización de estado"},
		"resolved": {"en": "Resolved", "fr": "Résolu", "de": "Behoben", "es": "Resuelto"},
	}
	common := map[string]subscriberCopy{
		"en": {
			subject: "Acme Status: API outage", reason: "You're receiving this because you subscribed to Acme Status.",
			unsubscribeLine: "Unsubscribe: https://status.example.com/api/v1/public/status-subscribers/unsubscribe?token=",
			moreInfo:        "More information: https://status.example.com/s/acme/acme-status",
		},
		"fr": {
			subject: "Acme Status : API outage",
			reason:  "Vous recevez cet e-mail dans le cadre de votre abonnement à Acme Status.",
			unsubscribeLine: "Se désabonner : " +
				"https://status.example.com/api/v1/public/status-subscribers/unsubscribe?token=",
			moreInfo: "Plus d'informations : https://status.example.com/s/acme/acme-status",
		},
		"de": {
			subject: "Acme Status: API outage", reason: "Sie erhalten diese E-Mail, weil Sie Acme Status abonniert haben.",
			unsubscribeLine: "Abbestellen: https://status.example.com/api/v1/public/status-subscribers/unsubscribe?token=",
			moreInfo:        "Weitere Informationen: https://status.example.com/s/acme/acme-status",
		},
		"es": {
			subject: "Acme Status: API outage", reason: "Recibes este correo porque te suscribiste a Acme Status.",
			unsubscribeLine: "Cancelar la suscripción: " +
				"https://status.example.com/api/v1/public/status-subscribers/unsubscribe?token=",
			moreInfo: "Más información: https://status.example.com/s/acme/acme-status",
		},
	}
	incidentUID := "incident-1"
	kinds := []struct {
		name         string
		kind         string
		priorUpdates int
	}{
		{"opened", string(models.StatusUpdateKindInvestigating), 0},
		{"update", string(models.StatusUpdateKindMonitoring), 2},
		{"resolved", string(models.StatusUpdateKindResolved), 3},
	}

	for _, kind := range kinds {
		for _, lang := range i18n.Supported() {
			t.Run(kind.name+"/"+lang, func(t *testing.T) {
				t.Parallel()
				r := require.New(t)

				s := newSubSetup(t)
				setPageLanguage(t, s, &lang)
				confirmedSubscriber(t, s, "reader@example.com")

				mail := notifyOne(t, s, kind.kind, &incidentUID, kind.priorUpdates)
				want := common[lang]
				label := labels[kind.name][lang]

				r.Equal(want.subject, mail.subject)
				r.Equal(lang, mail.locale)
				r.Equal("UTC", mail.timezone, "a status page has no timezone")
				r.Contains(mail.html, `<html lang="`+lang+`">`)
				r.Contains(mail.html, `<p class="eyebrow"`)
				r.Contains(mail.html, ">"+label+"</p>")
				r.Contains(mail.text, label+"\n\nAPI outage\n")
				r.Contains(mail.text, want.reason)
				r.Contains(mail.text, want.unsubscribeLine)
				r.Contains(mail.text, want.moreInfo)
			})
		}
	}
}

func TestSubscriberMailWithAnUnsupportedOrUnsetPageLanguageIsEnglish(t *testing.T) {
	t.Parallel()

	italian, regional, empty := "it", "fr-CA", ""

	cases := []struct {
		name     string
		language *string
		want     string
	}{
		{"null", nil, "en"},
		{"empty", &empty, "en"},
		{"unsupported", &italian, "en"},
		{"a regional tag is its language", &regional, "fr"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			s := newSubSetup(t)
			setPageLanguage(t, s, tc.language)
			confirmedSubscriber(t, s, "reader@example.com")

			mail := notifyOne(t, s, string(models.StatusUpdateKindResolved), nil, 1)
			r.Equal(tc.want, mail.locale)
			r.Contains(mail.html, `<html lang="`+tc.want+`">`)
		})
	}
}

// TestSubscriberMailPrefersTheReadersAccount: a subscriber whose address is a
// SolidPing account reads their own language and zone (spec decision 6); the
// page's language is only the hint for everyone else.
func TestSubscriberMailPrefersTheReadersAccount(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)
	french := "fr"
	setPageLanguage(t, s, &french)

	user := models.NewUser("member@example.com")
	user.Preferences = &models.UserPreferences{Locale: "de", Timezone: "Europe/Berlin"}
	r.NoError(s.dbSvc.CreateUser(t.Context(), user))

	confirmedSubscriber(t, s, "member@example.com")
	confirmedSubscriber(t, s, "stranger@example.com")

	sender := &fakeSender{}
	statussubscribers.NewNotifier(s.dbSvc, sender, testFormatter(t), "https://status.example.com", nil, nil, nil).
		NotifyStatusUpdate(t.Context(), statussubscribers.UpdateEventFor(
			s.page.UID, nil, string(models.StatusUpdateKindResolved), "All clear", "Done.", nil, "Acme Status", 1))

	byAddress := map[string]string{}
	for _, msg := range sender.sent() {
		byAddress[msg.Recipients.To[0]] = msg.Locale + "/" + msg.Timezone
	}

	r.Equal(map[string]string{
		"member@example.com":   "de/Europe/Berlin",
		"stranger@example.com": "fr/UTC",
	}, byAddress)
}

func TestSubscriberConfirmMailFollowsThePageLanguage(t *testing.T) {
	t.Parallel()

	want := map[string]struct{ subject, heading, button string }{
		"en": {"Confirm your subscription to Acme Status", "Confirm your subscription", "Confirm subscription"},
		"fr": {"Confirmez votre abonnement à Acme Status", "Confirmez votre abonnement", "Confirmer l&#39;abonnement"},
		"de": {"Bestätigen Sie Ihr Abonnement für Acme Status", "Bestätigen Sie Ihr Abonnement", "Abonnement bestätigen"},
		"es": {"Confirma tu suscripción a Acme Status", "Confirma tu suscripción", "Confirmar suscripción"},
	}

	for _, lang := range i18n.Supported() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			h := newHandlerSetup(t)
			setPageLanguage(t, h.subSetup, &lang)

			rec := h.do(t, http.MethodPost,
				"/api/v1/orgs/"+h.org.Slug+"/status-pages/"+h.page.UID+"/subscribers",
				map[string]any{"email": "visitor@example.com"})
			r.Equal(http.StatusAccepted, rec.Code)

			msgs := h.sender.sent()
			r.Len(msgs, 1)
			r.Equal(want[lang].subject, msgs[0].Subject)
			r.Equal(lang, msgs[0].Locale)
			r.Contains(msgs[0].HTML, `<html lang="`+lang+`">`)
			r.Contains(msgs[0].HTML, want[lang].heading+"</h1>")
			r.Contains(msgs[0].HTML, want[lang].button+"</a>")
			r.Contains(msgs[0].HTML, "<strong>Acme Status</strong>")
			r.True(strings.HasPrefix(msgs[0].Text, want[lang].subject+"\n\n"),
				"the text part opens with the subject line: %q", msgs[0].Text)
		})
	}
}

func TestSubscriberLandingPagesFollowThePageLanguage(t *testing.T) {
	t.Parallel()

	want := map[string]struct{ confirmed, unsubscribed, back string }{
		"en": {
			"<h1 style=\"font-size:22px;color:#1a1a2e;\">Subscription confirmed</h1>" +
				"<p>You&#39;re now subscribed to updates for Acme Status.</p>",
			"<h1 style=\"font-size:22px;color:#1a1a2e;\">Unsubscribed</h1>" +
				"<p>You will no longer receive updates for Acme Status.</p>",
			"Back to the status page",
		},
		"fr": {
			"<h1 style=\"font-size:22px;color:#1a1a2e;\">Abonnement confirmé</h1>" +
				"<p>Vous recevrez désormais les mises à jour concernant Acme Status.</p>",
			"<h1 style=\"font-size:22px;color:#1a1a2e;\">Désabonnement confirmé</h1>" +
				"<p>Vous ne recevrez plus les mises à jour concernant Acme Status.</p>",
			"Retour à la page de statut",
		},
		"de": {
			"<h1 style=\"font-size:22px;color:#1a1a2e;\">Abonnement bestätigt</h1>" +
				"<p>Ab sofort erhalten Sie Updates zu Acme Status.</p>",
			"<h1 style=\"font-size:22px;color:#1a1a2e;\">Abmeldung bestätigt</h1>" +
				"<p>Sie erhalten keine Updates mehr zu Acme Status.</p>",
			"Zurück zur Statusseite",
		},
		"es": {
			"<h1 style=\"font-size:22px;color:#1a1a2e;\">Suscripción confirmada</h1>" +
				"<p>A partir de ahora recibirás las actualizaciones de Acme Status.</p>",
			"<h1 style=\"font-size:22px;color:#1a1a2e;\">Suscripción cancelada</h1>" +
				"<p>Ya no recibirás actualizaciones de Acme Status.</p>",
			"Volver a la página de estado",
		},
	}

	for _, lang := range i18n.Supported() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			h := newHandlerSetup(t)
			setPageLanguage(t, h.subSetup, &lang)
			res := h.subscribe(t, "land@example.com")

			confirmed := h.do(t, http.MethodGet,
				"/api/v1/public/status-subscribers/confirm?token="+res.Subscriber.ConfirmToken, nil)
			r.Equal(http.StatusOK, confirmed.Code)
			r.Contains(confirmed.Body.String(), `<html lang="`+lang+`">`)
			r.Contains(confirmed.Body.String(), want[lang].confirmed)
			r.Contains(confirmed.Body.String(), ">"+want[lang].back+"</a>")

			unsubscribed := h.do(t, http.MethodGet,
				"/api/v1/public/status-subscribers/unsubscribe?token="+res.Subscriber.UnsubscribeToken, nil)
			r.Equal(http.StatusOK, unsubscribed.Code)
			r.Contains(unsubscribed.Body.String(), `<html lang="`+lang+`">`)
			r.Contains(unsubscribed.Body.String(), want[lang].unsubscribed)
		})
	}
}

// getLanding requests a landing page with the given Accept-Language header
// ("" sends none).
func getLanding(t *testing.T, h *handlerSetup, path, acceptLanguage string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	if acceptLanguage != "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	return rec
}

const (
	confirmPath     = "/api/v1/public/status-subscribers/confirm?token="
	unsubscribePath = "/api/v1/public/status-subscribers/unsubscribe?token="
)

// TestSubscriberLandingPageWithAnUnsupportedOrUnsetPageLanguageIsEnglish: the
// page's language decides, normalized, and the browser's Accept-Language
// (German here) does not override a page that is known. The regional tag is
// the positive control.
func TestSubscriberLandingPageWithAnUnsupportedOrUnsetPageLanguageIsEnglish(t *testing.T) {
	t.Parallel()

	italian, regional, empty := "it", "fr-FR", ""

	cases := []struct {
		name                    string
		language                *string
		lang, confirmed, unsubd string
	}{
		{"null", nil, "en", "Subscription confirmed", "Unsubscribed"},
		{"empty", &empty, "en", "Subscription confirmed", "Unsubscribed"},
		{"unsupported", &italian, "en", "Subscription confirmed", "Unsubscribed"},
		{"a regional tag is its language", &regional, "fr", "Abonnement confirmé", "Désabonnement confirmé"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			h := newHandlerSetup(t)
			setPageLanguage(t, h.subSetup, tc.language)
			res := h.subscribe(t, "land@example.com")

			confirmed := getLanding(t, h, confirmPath+res.Subscriber.ConfirmToken, "de")
			r.Equal(http.StatusOK, confirmed.Code)
			r.Contains(confirmed.Body.String(), `<html lang="`+tc.lang+`">`)
			r.Contains(confirmed.Body.String(), ">"+tc.confirmed+"</h1>")

			unsubscribed := getLanding(t, h, unsubscribePath+res.Subscriber.UnsubscribeToken, "de")
			r.Equal(http.StatusOK, unsubscribed.Code)
			r.Contains(unsubscribed.Body.String(), `<html lang="`+tc.lang+`">`)
			r.Contains(unsubscribed.Body.String(), ">"+tc.unsubd+"</h1>")
		})
	}
}

// TestSubscriberLandingPagesPreferTheReadersAccount: the links of a mail land
// on a page in the mail's language, so an account's own language wins over the
// page's there too. The stranger on the same page is the positive control.
func TestSubscriberLandingPagesPreferTheReadersAccount(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h := newHandlerSetup(t)
	french := "fr"
	setPageLanguage(t, h.subSetup, &french)

	user := models.NewUser("member@example.com")
	user.Preferences = &models.UserPreferences{Locale: "de", Timezone: "Europe/Berlin"}
	r.NoError(h.dbSvc.CreateUser(t.Context(), user))

	rec := h.do(t, http.MethodPost,
		"/api/v1/orgs/"+h.org.Slug+"/status-pages/"+h.page.UID+"/subscribers",
		map[string]any{"email": "member@example.com"})
	r.Equal(http.StatusAccepted, rec.Code)
	r.Len(h.sender.sent(), 1)
	r.Equal("de", h.sender.sent()[0].Locale, "the confirm mail is German")

	member := h.subscribe(t, "member@example.com")
	stranger := h.subscribe(t, "stranger@example.com")

	confirmed := getLanding(t, h, confirmPath+member.Subscriber.ConfirmToken, "")
	r.Contains(confirmed.Body.String(), `<html lang="de">`)
	r.Contains(confirmed.Body.String(), "Abonnement bestätigt</h1>")

	unsubscribed := getLanding(t, h, unsubscribePath+member.Subscriber.UnsubscribeToken, "")
	r.Contains(unsubscribed.Body.String(), `<html lang="de">`)
	r.Contains(unsubscribed.Body.String(), "Abmeldung bestätigt</h1>")

	control := getLanding(t, h, confirmPath+stranger.Subscriber.ConfirmToken, "")
	r.Contains(control.Body.String(), `<html lang="fr">`)
	r.Contains(control.Body.String(), "Abonnement confirmé</h1>")
}

// TestSubscriberLandingPageOfAnUnknownLinkFollowsTheBrowser: a used, unknown or
// failed link names no page, so the browser's preferred supported language
// stands in, and English when it prefers none.
func TestSubscriberLandingPageOfAnUnknownLinkFollowsTheBrowser(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, acceptLanguage, lang, title string
	}{
		{"no header", "", "en", "Link expired"},
		{"unsupported", "it-IT, it;q=0.9", "en", "Link expired"},
		{"malformed", "fr;q=x", "en", "Link expired"},
		{"regional", "fr-CH, fr;q=0.9, en;q=0.8", "fr", "Lien expiré"},
		{"first supported by weight", "it, en;q=0.3, de;q=0.5", "de", "Link abgelaufen"},
		{"a zero weight is a refusal", "fr;q=0, es;q=0.2", "es", "Enlace caducado"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			h := newHandlerSetup(t)
			rec := getLanding(t, h, confirmPath+"bogus", tc.acceptLanguage)
			r.Equal(http.StatusBadRequest, rec.Code)
			r.Contains(rec.Body.String(), `<html lang="`+tc.lang+`">`)
			r.Contains(rec.Body.String(), "<title>"+tc.title+"</title>")
			r.Contains(rec.Header().Values("Vary"), "Accept-Language")
		})
	}
}

// TestSubscriberLandingPageOfAUsedLinkFollowsTheBrowser: a second click on a
// confirm or unsubscribe link finds no subscriber any more, so its page no
// longer knows the status page's language: the browser's stands in, and
// without one the page is English. The first clicks are the positive control.
func TestSubscriberLandingPageOfAUsedLinkFollowsTheBrowser(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h := newHandlerSetup(t)
	french := "fr"
	setPageLanguage(t, h.subSetup, &french)
	res := h.subscribe(t, "land@example.com")

	confirmed := getLanding(t, h, confirmPath+res.Subscriber.ConfirmToken, "")
	r.Contains(confirmed.Body.String(), `<html lang="fr">`)

	again := getLanding(t, h, confirmPath+res.Subscriber.ConfirmToken, "")
	r.Equal(http.StatusBadRequest, again.Code, "a used confirm link has no page any more")
	r.Contains(again.Body.String(), `<html lang="en">`)
	r.Contains(again.Body.String(), "<title>Link expired</title>")

	again = getLanding(t, h, confirmPath+res.Subscriber.ConfirmToken, "fr-FR,fr;q=0.9")
	r.Equal(http.StatusBadRequest, again.Code)
	r.Contains(again.Body.String(), `<html lang="fr">`)
	r.Contains(again.Body.String(), "<title>Lien expiré</title>")

	unsubscribed := getLanding(t, h, unsubscribePath+res.Subscriber.UnsubscribeToken, "")
	r.Contains(unsubscribed.Body.String(), "Désabonnement confirmé</h1>")

	again = getLanding(t, h, unsubscribePath+res.Subscriber.UnsubscribeToken, "es")
	r.Equal(http.StatusBadRequest, again.Code)
	r.Contains(again.Body.String(), `<html lang="es">`)
	r.Contains(again.Body.String(), "<title>Enlace caducado</title>")
}
