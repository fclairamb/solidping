package emailpreview

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// nb is the no-break space fr, de and es put between a number and its unit.
const nb = " "

// incidentLocaleCase is one incident template: its subject and a sentence of
// its plaintext part, per language.
type incidentLocaleCase struct {
	template string
	subject  map[string]string
	text     map[string]string
}

// incidentLocaleCases covers the 12 incident templates. The fixtures are built
// by the mailer's own view model, so these are the words a real mail carries.
func incidentLocaleCases() []incidentLocaleCase {
	return []incidentLocaleCase{
		{
			template: "incident-created.html",
			subject: map[string]string{
				"fr": "🔴 Production API est en panne : HTTP 503 Service Unavailable (expected 200) (#42)",
				"de": "🔴 Production API ist ausgefallen: HTTP 503 Service Unavailable (expected 200) (#42)",
				"es": "🔴 Production API está caído: HTTP 503 Service Unavailable (expected 200) (#42)",
			},
			text: map[string]string{
				"fr": "Régions en échec : 🇪🇺 Europe West (1 région sur 3)",
				"de": "Betroffene Regionen: 🇪🇺 Europe West (1 von 3 Regionen)",
				"es": "Regiones con fallos: 🇪🇺 Europe West (1 de 3 regiones)", //nolint:misspell // Spanish copy
			},
		},
		{
			template: "incident-escalated.html",
			subject: map[string]string{
				"fr": "🟠 Production API est toujours en panne, incident escaladé (#42)",
				"de": "🟠 Production API ist weiterhin ausgefallen, eskaliert (#42)",
				"es": "🟠 Production API sigue caído, incidente escalado (#42)",
			},
			text: map[string]string{
				"fr": "L'incident de Production API a été escaladé.",
				"de": "Der Vorfall für Production API wurde eskaliert.",
				"es": "El incidente de Production API se ha escalado.",
			},
		},
		{
			template: "incident-reopened.html",
			subject: map[string]string{
				"fr": "🔴 Production API est de nouveau en panne (#42)",
				"de": "🔴 Production API ist erneut ausgefallen (#42)",
				"es": "🔴 Production API vuelve a estar caído (#42)",
			},
			text: map[string]string{
				"fr": "Incident rouvert sur Production API (rechute n°" + nb + "1)",
				"de": "Vorfall für Production API wiedereröffnet (Rückfall Nr. 1)",
				"es": "Incidente de Production API reabierto (recaída n.º 1)",
			},
		},
		{
			template: "incident-resolved.html",
			subject: map[string]string{
				"fr": "🟢 Production API fonctionne à nouveau après 15" + nb + "min de panne (#42)",
				"de": "🟢 Production API läuft nach 15" + nb + "Min. Ausfall wieder (#42)",
				"es": "🟢 Production API vuelve a funcionar tras 15" + nb + "min de caída (#42)",
			},
			text: map[string]string{
				"fr": "L'incident de Production API est résolu.",
				"de": "Der Vorfall für Production API wurde gelöst.",
				"es": "El incidente de Production API se ha resuelto.",
			},
		},
		{
			template: "incident-acknowledged.html",
			subject: map[string]string{
				"fr": "🔵 Alice Admin s'occupe de Production API (#42)",
				"de": "🔵 Alice Admin kümmert sich um Production API (#42)",
				"es": "🔵 Alice Admin se encarga de Production API (#42)",
			},
			text: map[string]string{
				"fr": "Alice Admin a acquitté l'incident de Production API via Slack. " +
					"L'escalade est arrêtée : personne d'autre n'a besoin de s'en charger.",
				"de": "Alice Admin hat den Vorfall für Production API über Slack bestätigt. " +
					"Die Eskalation ist gestoppt: Niemand sonst muss ihn übernehmen.",
				"es": "Alice Admin reconoció el incidente de Production API vía Slack. " +
					"El escalado se ha detenido: nadie más tiene que encargarse.",
			},
		},
		{
			template: "incident-unacknowledged.html",
			subject: map[string]string{
				"fr": "🟠 Plus personne ne s'occupe de Production API (#42)",
				"de": "🟠 Niemand kümmert sich mehr um Production API (#42)",
				"es": "🟠 Ya nadie se encarga de Production API (#42)",
			},
			text: map[string]string{
				"fr": "Alice Admin a annulé l'acquittement de l'incident de Production API via Slack.",
				"de": "Alice Admin hat die Bestätigung für den Vorfall von Production API über Slack aufgehoben.",
				"es": "Alice Admin anuló el reconocimiento del incidente de Production API vía Slack.",
			},
		},
		{
			template: "incident-comment.html",
			subject: map[string]string{
				"fr": "💬 Commentaire de Bob Builder sur Production API (#42)",
				"de": "💬 Kommentar von Bob Builder zu Production API (#42)",
				"es": "💬 Comentario de Bob Builder en Production API (#42)",
			},
			text: map[string]string{
				"fr": "Bob Builder a commenté l'incident de Production API via Slack :",
				"de": "Bob Builder hat den Vorfall von Production API über Slack kommentiert:",
				"es": "Bob Builder comentó el incidente de Production API vía Slack:",
			},
		},
		{
			template: "incident-reminder.html",
			subject: map[string]string{
				"fr": "🔴 Production API est toujours en panne, personne ne s'en occupe (#42)",
				"de": "🔴 Production API ist weiterhin ausgefallen, niemand kümmert sich darum (#42)",
				"es": "🔴 Production API sigue caído y nadie se encarga (#42)",
			},
			text: map[string]string{
				"fr": "Toujours en panne depuis 1" + nb + "h 15" + nb + "min, non acquitté (relance 3).",
				"de": "Seit 1" + nb + "Std. 15" + nb + "Min. ausgefallen, nicht bestätigt (Erinnerung 3).",
				"es": "Sigue caído tras 1" + nb + "h 15" + nb + "min, sin reconocer (recordatorio 3).",
			},
		},
		{
			template: "incident-burn-created.html",
			subject: map[string]string{
				"fr": "🔥 Combustion rapide : Acme API availability à 31,0x (#42)",
				"de": "🔥 Schneller Verbrauch: Acme API availability bei 31,0x (#42)",
				"es": "🔥 Consumo rápido: Acme API availability a 31,0x (#42)",
			},
			text: map[string]string{
				"fr": "Politique : Combustion rapide, critique",
				"de": "Richtlinie: Schneller Verbrauch, kritisch",
				"es": "Política: Consumo rápido, crítico",
			},
		},
		{
			template: "incident-burn-resolved.html",
			subject: map[string]string{
				"fr": "🟢 Acme API availability : de nouveau sous 14,4x (#42)",
				"de": "🟢 Acme API availability wieder unter 14,4x (#42)",
				"es": "🟢 Acme API availability vuelve a estar por debajo de 14,4x (#42)",
			},
			text: map[string]string{
				"fr": "Le budget consommé n'est pas restitué : seule la vitesse de consommation est redescendue.",
				"de": "Das verbrauchte Budget wird nicht zurückerstattet: Nur die Verbrauchsrate ist wieder gesunken.",
				"es": "El presupuesto consumido no se devuelve: solo ha bajado la tasa de consumo.",
			},
		},
		{
			template: "incident-degraded-created.html",
			subject: map[string]string{
				"fr": "🟡 Production API fonctionne en mode dégradé : 7 échecs sur les 60 dernières sondes (93,8" + nb +
					"% de disponibilité) et 3 des 6 dernières sondes ont dépassé 1000" + nb + "ms (#42)",
				"de": "🟡 Production API ist beeinträchtigt: 7 Fehler in den letzten 60 Probes (93,8" + nb +
					"% verfügbar) und 3 der letzten 6 Probes waren langsamer als 1000" + nb + "ms (#42)",
				"es": "🟡 Production API está degradado: 7 fallos en las últimas 60 sondas (93,8" + nb +
					"% de disponibilidad) y 3 de las últimas 6 sondas tardaron más de 1000" + nb + "ms (#42)",
			},
			text: map[string]string{
				"fr": "Il ne s'agit pas d'une panne : chaque échec a été suivi d'un succès " +
					"avant la fin de la période de confirmation.",
				"de": "Dies ist kein Ausfall: Auf jeden Fehler folgte ein Erfolg, " +
					"bevor die Bestätigungsfrist abgelaufen war.",
				"es": "No es una caída: cada fallo fue seguido de un éxito " +
					"antes de que terminara el periodo de confirmación.",
			},
		},
		{
			template: "incident-degraded-resolved.html",
			subject: map[string]string{
				"fr": "🟢 Production API est de nouveau stable (#42)",
				"de": "🟢 Production API ist wieder stabil (#42)",
				"es": "🟢 Production API vuelve a estar estable (#42)",
			},
			text: map[string]string{
				"fr": "La condition de dégradation n'est plus remplie depuis assez longtemps pour clore l'épisode.",
				"de": "Die Beeinträchtigungsbedingung ist lange genug nicht mehr erfüllt, um die Phase zu schließen.",
				"es": "La condición de degradación lleva el tiempo suficiente sin cumplirse para cerrar el episodio.",
			},
		},
	}
}

// TestIncidentTemplatesInTheReadersLanguage renders every incident template
// for a French, a German and a Spanish reader, each in their own timezone: the
// subject, a sentence of the plaintext part and the incident start (in the
// reader's zone) are theirs, and no English line of the old text blocks is
// left.
func TestIncidentTemplatesInTheReadersLanguage(t *testing.T) {
	t.Parallel()

	formatter := incidentFormatter(t)

	readers := map[string]i18n.Locale{
		i18n.LangFrench:  i18n.New(i18n.LangFrench, "Europe/Paris"),
		i18n.LangGerman:  i18n.New(i18n.LangGerman, "Europe/Berlin"),
		i18n.LangSpanish: i18n.New(i18n.LangSpanish, "America/Mexico_City"),
	}
	// startedAt is the fixtures' Jul 5, 10:00 UTC, where each reader lives.
	startedAt := map[string]string{
		i18n.LangFrench:  "5 juil., 12:00 CEST",
		i18n.LangGerman:  "5. Juli, 12:00 CEST",
		i18n.LangSpanish: "5 jul, 04:00 CST",
	}
	// zone is the reader's: every date and time of the HTML part carries it.
	zone := map[string]string{i18n.LangFrench: " CEST", i18n.LangGerman: " CEST", i18n.LangSpanish: " CST"}

	cases := incidentLocaleCases()
	require.Len(t, cases, len(incidentTemplates(t)), "one case per incident template")

	for _, tc := range cases {
		for lang, loc := range readers {
			t.Run(tc.template+"/"+lang, func(t *testing.T) {
				t.Parallel()
				r := require.New(t)

				data, ok := fixtureFor(tc.template, loc)
				r.True(ok)

				subject, html, text, err := formatter.Format(tc.template, loc, data)
				r.NoError(err)

				r.Equal(tc.subject[lang], subject)
				r.Contains(text, tc.text[lang])
				r.Contains(text, startedAt[lang])
				r.Contains(html, zone[lang])
				r.NotContains(html, " UTC")

				for _, english := range englishTextLines() {
					r.NotContains(text, english)
				}
			})
		}
	}
}

// TestIncidentTextBlocksUseTheSharedPartials: the plaintext parts end with the
// shared action and footer lines, so the old "(link valid for 7 days)" is
// gone and the ack link states its real expiry. It is also the positive
// control of the test above: English carries what translations must not.
func TestIncidentTextBlocksUseTheSharedPartials(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	formatter := incidentFormatter(t)

	for _, name := range incidentTemplates(t) {
		data, ok := fixtureFor(name, i18n.English)
		r.True(ok, name)

		_, html, text, err := formatter.Format(name, i18n.English, data)
		r.NoError(err, name)
		r.Contains(html, " UTC", name)

		for _, english := range englishTextLines() {
			r.Contains(text, english, name)
		}

		r.NotContains(text, "link valid for", name)
		r.NotContains(text, "Acknowledge this incident", name)
		r.Contains(text, "This is an automated notification from SolidPing.", name)
		r.Contains(text, "Stop alerts for Production API: ", name)

		// The degraded notice offers the chart, not the ack link.
		if ack, _ := data["AckURL"].(string); ack != "" && name != "incident-degraded-created.html" {
			r.Contains(text, "The link works until Jul 12, 10:00 UTC.", name)
		}
	}

	data, _ := fixtureFor("incident-resolved.html", i18n.English)
	_, _, text, err := formatter.Format("incident-resolved.html", i18n.English, data)
	r.NoError(err)
	r.Contains(text, "Resolved at: "+fixtureResolvedAt+"\n")
}

// TestIncidentTemplatesSurviveTheEmailJob: incident-created.html is also sent
// through the email job (the test API's "incident" mail), which decodes the
// view model as JSON: numbers arrive as float64, dates as RFC 3339 strings,
// durations as nanoseconds. The mail must still render, in the reader's
// language and zone.
func TestIncidentTemplatesSurviveTheEmailJob(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	formatter := incidentFormatter(t)

	loc := i18n.New(i18n.LangFrench, "Europe/Paris")

	for name, want := range map[string]string{
		"incident-created.html":  "🔴 Production API est en panne : HTTP 503 Service Unavailable (expected 200) (#42)",
		"incident-resolved.html": "🟢 Production API fonctionne à nouveau après 15" + nb + "min de panne (#42)",
	} {
		data, ok := fixtureFor(name, loc)
		r.True(ok, name)

		raw, err := json.Marshal(data)
		r.NoError(err, name)

		var decoded map[string]any
		r.NoError(json.Unmarshal(raw, &decoded), name)

		subject, html, text, err := formatter.Format(name, loc, decoded)
		r.NoError(err, name)
		r.Equal(want, subject, name)
		r.Contains(html, "5 juil., 12:00 CEST", name)
		r.Contains(text, "Incident : #42", name)
	}
}

// incidentFormatter is the production formatter, with no base URL.
func incidentFormatter(t *testing.T) *email.TemplateFormatter {
	t.Helper()

	formatter, err := email.NewFormatter()
	require.NoError(t, err)

	return formatter
}

// englishTextLines are words every English incident text part carries, and
// no translated one may.
func englishTextLines() []string {
	return []string{"Started at", "Incident ID", "automated notification", "View incident"}
}

// incidentTemplates is every shipped incident-* template.
func incidentTemplates(t *testing.T) []string {
	t.Helper()

	var out []string

	for _, name := range FixtureTemplateNames() {
		if strings.HasPrefix(name, "incident-") {
			out = append(out, name)
		}
	}

	require.Len(t, out, 12)

	return out
}
