package emailpreview

import (
	"encoding/json"
	"html"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/regionsweep"
)

// alertsZone is a reader's zone that is never UTC, so a date that ignores it
// shows.
const alertsZone = "America/New_York"

// alertsExpectations is, per alert template and language, the subject and one
// sentence of the plaintext part.
//
//nolint:gochecknoglobals // read-only test table.
var alertsExpectations = map[string]map[string][2]string{
	"escalation.html": {
		"en": {
			"🟠 Production API is still down, escalated to you (#42)",
			"Nobody acknowledged it in time, so it was escalated to you.",
		},
		"fr": {
			"🟠 Production API est toujours en panne, incident escaladé vers vous (#42)",
			"Personne n'a acquitté l'incident à temps, il vous a donc été escaladé.",
		},
		"de": {
			"🟠 Production API ist weiterhin ausgefallen, an Sie eskaliert (#42)",
			"Niemand hat den Vorfall rechtzeitig bestätigt, daher wurde er an Sie eskaliert.",
		},
		"es": {
			"🟠 Production API sigue fallando, se te ha escalado el incidente (#42)",
			"Nadie ha reconocido el incidente a tiempo, así que se te ha escalado.",
		},
	},
	"paging-nudge.html": {
		"en": {
			"Set up your alert notifications for Acme Corp",
			"An administrator of Acme Corp asked you to set up how SolidPing should reach you",
		},
		"fr": {
			"Configurez vos notifications d'alerte pour Acme Corp",
			"Un administrateur de l'organisation Acme Corp vous demande de configurer",
		},
		"de": {
			"Richten Sie Ihre Benachrichtigungen für Acme Corp ein",
			"Ein Administrator von Acme Corp bittet Sie, festzulegen, wie SolidPing Sie erreichen soll",
		},
		"es": {
			"Configura tus notificaciones de alerta para Acme Corp",
			"Un administrador de Acme Corp te ha pedido que configures cómo debe avisarte SolidPing",
		},
	},
	"test-email.html": {
		"en": {"SolidPing Test Email", "If you received this, your email configuration is working correctly."},
		"fr": {"E-mail de test SolidPing", "Si vous l'avez reçu, votre configuration e-mail fonctionne correctement."},
		"de": {"SolidPing-Test-E-Mail", "Dies ist eine Test-E-Mail von SolidPing. Wenn Sie diese E-Mail erhalten haben,"},
		"es": {"Correo de prueba de SolidPing", "Si lo has recibido, tu configuración de correo funciona correctamente."},
	},
	"region-offline.html": {
		"en": {
			"🔴 Region Lauterbourg (lauterbourg) is offline: 2 of your checks are not running",
			"2 of your checks in Acme Corp run only from this region, so they are not being checked right now:",
		},
		"fr": {
			"🔴 La région Lauterbourg (lauterbourg) est hors ligne : 2 de vos contrôles ne s'exécutent plus",
			"2 de vos contrôles dans l'organisation Acme Corp s'exécutent uniquement depuis cette région",
		},
		"de": {
			"🔴 Region Lauterbourg (lauterbourg) ist offline: 2 Ihrer Prüfungen laufen nicht",
			"2 Ihrer Prüfungen in Acme Corp laufen nur von dieser Region aus",
		},
		"es": {
			"🔴 La región Lauterbourg (lauterbourg) está fuera de línea: 2 de tus comprobaciones no se ejecutan",
			"2 de tus comprobaciones en Acme Corp se ejecutan solo desde esta región",
		},
	},
	"region-recovered.html": {
		"en": {"🟢 Region Lauterbourg (lauterbourg) is back, your checks resumed", "Offline for: 7h 52m"},
		"fr": {
			"🟢 La région Lauterbourg (lauterbourg) est rétablie, vos contrôles ont repris",
			"Hors ligne pendant : 7 h 52 min",
		},
		"de": {
			"🟢 Region Lauterbourg (lauterbourg) ist wieder online, Ihre Prüfungen werden fortgesetzt",
			"Offline-Dauer: 7 Std. 52 Min.",
		},
		"es": {
			"🟢 La región Lauterbourg (lauterbourg) ha vuelto, tus comprobaciones se han reanudado",
			"Tiempo fuera de línea: 7 h 52 min",
		},
	},
	"usage-cap-reached.html": {
		"en": {
			"🟠 Acme Corp used all its SMS alerts for this month",
			"Acme Corp has used its 20 SMS alerts included this month.",
		},
		"fr": {
			"🟠 Acme Corp a utilisé toutes ses alertes SMS du mois",
			"Acme Corp a épuisé son quota de 20 alertes SMS ce mois-ci.",
		},
		"de": {
			"🟠 Acme Corp hat alle SMS-Benachrichtigungen dieses Monats aufgebraucht",
			"Acme Corp hat das Monatskontingent von 20 SMS-Benachrichtigungen aufgebraucht.",
		},
		"es": {
			"🟠 Acme Corp ha agotado sus alertas por SMS de este mes",
			"Acme Corp ha agotado su cuota de 20 alertas por SMS de este mes.",
		},
	},
	"custom-domain-demoted.html": {
		"en": {"🟠 Your status page stopped serving on status.acme.com", "then click Verify."},
		"fr": {"🟠 Votre page de statut n'est plus servie sur status.acme.com", "puis cliquez sur Vérifier."},
		"de": {
			"🟠 Ihre Statusseite wird nicht mehr über status.acme.com ausgeliefert",
			"und klicken Sie dann auf Überprüfen.",
		},
		"es": {"🟠 Tu página de estado ya no se sirve en status.acme.com", "y luego haz clic en Verificar."},
	},
}

// alertsDates is, per template that shows dates, the fixture instants it
// prints, so the test can check they are in the reader's zone and language.
//
//nolint:gochecknoglobals // read-only test table.
var alertsDates = map[string][]time.Time{
	"escalation.html":        {fixtureAlertTime(time.July, 5, 10, 0), fixtureAckExpiresAt()},
	"region-offline.html":    {fixtureAlertTime(time.September, 24, 13, 41)},
	"region-recovered.html":  {fixtureAlertTime(time.September, 24, 13, 41), fixtureAlertTime(time.September, 24, 21, 33)},
	"usage-cap-reached.html": {fixtureAlertTime(time.November, 1, 0, 0)},
}

// TestAlerts_EveryTemplateSpeaksEveryLanguage renders each alert template in
// each language for a reader in New York: translated subject and body, dates
// in the reader's zone and language.
func TestAlerts_EveryTemplateSpeaksEveryLanguage(t *testing.T) {
	t.Parallel()

	formatter, err := email.NewFormatter()
	require.NoError(t, err)

	for tmpl, byLang := range alertsExpectations {
		for lang, want := range byLang {
			t.Run(tmpl+"/"+lang, func(t *testing.T) {
				t.Parallel()
				r := require.New(t)

				loc := i18n.New(lang, alertsZone)
				data, ok := fixtureFor(tmpl, loc)
				r.True(ok)

				subject, html, text, err := formatter.Format(tmpl, loc, data)
				r.NoError(err)
				r.Equal(want[0], subject)
				r.Contains(text, want[1])
				r.Contains(html, `<html lang="`+lang+`">`)
				r.NotContains(text, "alerts.", "a raw catalog key leaked")

				for _, when := range alertsDates[tmpl] {
					r.Contains(text, loc.Date(when), "dates follow the reader")
				}

				if lang != i18n.LangEnglish && len(alertsDates[tmpl]) > 0 {
					r.NotContains(text, i18n.English.Date(alertsDates[tmpl][0]))
				}
			})
		}
	}
}

// jobSentAlerts are the alert templates sent through the email job, whose
// view model is decoded from JSON before the render.
//
//nolint:gochecknoglobals // read-only test table.
var jobSentAlerts = []string{
	"region-offline.html", "region-recovered.html", "usage-cap-reached.html", "custom-domain-demoted.html",
}

// TestAlerts_JobRoundTripRendersTheSame: after the email job's JSON round trip
// (ints become float64, times RFC 3339 strings, durations nanosecond counts),
// a job-sent template renders exactly as from the Go values.
func TestAlerts_JobRoundTripRendersTheSame(t *testing.T) {
	t.Parallel()

	formatter, err := email.NewFormatter()
	require.NoError(t, err)

	for _, tmpl := range jobSentAlerts {
		for _, lang := range i18n.Supported() {
			t.Run(tmpl+"/"+lang, func(t *testing.T) {
				t.Parallel()
				r := require.New(t)

				loc := i18n.New(lang, alertsZone)
				data, ok := fixtureFor(tmpl, loc)
				r.True(ok)

				raw, err := json.Marshal(data)
				r.NoError(err)

				var decoded map[string]any
				r.NoError(json.Unmarshal(raw, &decoded))

				subject, html, text, err := formatter.Format(tmpl, loc, data)
				r.NoError(err)

				jobSubject, jobHTML, jobText, err := formatter.Format(tmpl, loc, decoded)
				r.NoError(err)
				r.Equal(subject, jobSubject)
				r.Equal(text, jobText)
				r.Equal(html, jobHTML)
			})
		}
	}
}

// TestAlerts_RegionOfflineSingularAfterRoundTrip is the bug the old
// {{if eq .BlindCount 1}} had: a float64 count could not be compared and the
// mail was lost. The singular now comes from the plural forms, and the closing
// sentence, which has no count, from the SingleBlind flag.
func TestAlerts_RegionOfflineSingularAfterRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	data := regionsweep.OfflineEmailData(fixtureRegionName, fixtureAlertTime(time.September, 24, 13, 41), 1, 1,
		[]regionsweep.NoticeCheck{{Name: "API"}}, "")
	data[keyOrgName] = fixtureOrgName

	raw, err := json.Marshal(data)
	r.NoError(err)

	var decoded map[string]any
	r.NoError(json.Unmarshal(raw, &decoded))

	want := map[string][3]string{
		"en": {
			"1 of your checks is not running", "1 other check keeps running from its other regions.",
			"Until then this check will show no data rather than a status.",
		},
		"fr": {
			"1 de vos contrôles ne s'exécute plus", "1 autre contrôle continue de s'exécuter",
			"D'ici là, ce contrôle n'affichera pas de statut, faute de données.",
		},
		"de": {
			"1 Ihrer Prüfungen läuft nicht", "1 weitere Prüfung läuft in ihren übrigen Regionen weiter",
			"Bis dahin zeigt diese Prüfung statt eines Status",
		},
		"es": {
			"1 de tus comprobaciones no se ejecuta", "1 comprobación más sigue ejecutándose",
			"Hasta entonces, esta comprobación aparecerá sin datos",
		},
	}

	for lang, sentences := range want {
		subject, body, text, err := formatter.Format("region-offline.html", i18n.New(lang, ""), decoded)
		r.NoError(err, lang)
		r.Contains(subject, sentences[0], lang)
		r.Contains(text, sentences[1], lang)
		r.Contains(text, sentences[2], lang)
		r.Contains(body, html.EscapeString(sentences[2]), lang)
	}
}

// TestAlerts_UsageCapJobQueuedBeforeTheUpgrade: a usage-cap job queued by the
// previous release carries the channel as a display label ("SMS", "voice
// call", "WhatsApp") and no ResetsAt. It still renders whole sentences, never
// a raw catalog key, and states the reset as the 1st of next month.
func TestAlerts_UsageCapJobQueuedBeforeTheUpgrade(t *testing.T) {
	t.Parallel()

	formatter, err := email.NewFormatter()
	require.NoError(t, err)

	want := map[string][2]string{
		"SMS":            {"🟠 Acme used all its SMS alerts for this month", "Acme has used its 20 SMS alerts included"},
		"voice call":     {"🟠 Acme used all its voice call alerts for this month", "Acme has used its 20 voice call alerts"},
		"WhatsApp":       {"🟠 Acme used all its WhatsApp alerts for this month", "Acme has used its 20 WhatsApp alerts"},
		"carrier pigeon": {"🟠 Acme used all its SMS alerts for this month", "Acme has used its 20 SMS alerts"},
	}

	for channel, sentences := range want {
		t.Run(channel, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			legacy := map[string]any{
				"Channel": channel, "Limit": float64(20), "OrgName": "Acme",
				"UsageURL": "https://solidping.example/d/orgs/acme/organization/usage", "UpgradeURL": "",
			}

			subject, body, text, renderErr := formatter.Format("usage-cap-reached.html", i18n.English, legacy)
			r.NoError(renderErr)
			r.Equal(sentences[0], subject)
			r.Contains(text, sentences[1])
			r.Contains(text, "will drop the next ones until the 1st of next month (UTC).")
			r.NotContains(subject+body+text, "alerts.", "a raw catalog key leaked")
		})
	}

	fr, _, frText, err := formatter.Format("usage-cap-reached.html", i18n.New("fr", ""),
		map[string]any{"Channel": "voice call", "Limit": float64(20), "OrgName": "Acme"})
	require.NoError(t, err)
	require.Equal(t, "🟠 Acme a utilisé toutes ses alertes par appel vocal du mois", fr)
	require.Contains(t, frText, "jusqu'au 1er du mois prochain (UTC).")
}

// TestAlerts_EscalationHeadingLinkIsSchemeFiltered: the heading's check link
// goes through a catalog value, which only HTML-escapes it, so the template
// filters the scheme itself: a URL that is not http(s) or site-relative is
// not linked.
func TestAlerts_EscalationHeadingLinkIsSchemeFiltered(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	data := escalationFixture(i18n.English)

	_, html, _, err := formatter.Format("escalation.html", i18n.English, data)
	r.NoError(err)
	r.Contains(html, `<a href="`+fixtureCheckURL+`"`, "positive control: an https check URL is linked")

	data["CheckURL"] = "javascript:alert(1)"
	_, html, _, err = formatter.Format("escalation.html", i18n.New("fr", ""), data)
	r.NoError(err)
	r.NotContains(html, "javascript:")
	r.Contains(html, "Production API est toujours en panne</h1>")
}

// TestAlerts_UsageCapWithoutChannelReadsSMS: a view model missing its channel
// (never produced, but the template must not build a key from nil) reads as
// the SMS notice.
func TestAlerts_UsageCapWithoutChannelReadsSMS(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	subject, _, _, err := formatter.Format("usage-cap-reached.html", i18n.English, map[string]any{"OrgName": "Acme"})
	r.NoError(err)
	r.Equal("🟠 Acme used all its SMS alerts for this month", subject)
}
