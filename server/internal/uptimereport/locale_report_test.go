package uptimereport_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
)

// nbsp is the no-break space fr, de and es put between a number and its unit.
const nbsp = " "

// preheaderPattern reads the hidden preheader base.html puts first in the
// body, up to the zero-width padding that follows it.
var preheaderPattern = regexp.MustCompile(`(?s)display:none[^>]*>\s*(.*?)\s*(?:\x{200B}|&#8203;)`)

type localeWant struct {
	subject, preheader, title, overall, legend, badge string
	// english is copy that must not survive in this language's render.
	english []string
}

func localeWants() map[string]localeWant {
	leftovers := []string{
		"Uptime report", "Availability", "Response time", "Objectives", "Open dashboard",
		"worst first", "No data", "Checks monitored", "Total downtime", "Budget remaining",
		" vs ", "uptime", "longest", "average", "samples", "Gray means",
	}

	return map[string]localeWant{
		i18n.LangEnglish: {
			subject:   "acme, July 2026: 99.950% uptime",
			preheader: "99.950% availability · 3 incidents · July 2026.",
			title:     "Uptime report",
			overall:   "Overall availability: 99.950% (+0.080 pts vs June 2026: 99.870%)",
			legend:    "Daily availability, Jul 1 – Jul 31 (UTC): green ≥ 99.9%, amber ≥ 99%, orange ≥ 95%, red below.",
			badge:     "Healthy",
		},
		i18n.LangFrench: {
			subject:   "acme, juillet 2026 : 99,950" + nbsp + "% de disponibilité",
			preheader: "99,950" + nbsp + "% de disponibilité · 3 incidents · juillet 2026.",
			title:     "Rapport de disponibilité",
			overall: "Disponibilité globale : 99,950" + nbsp + "% (+0,080" + nbsp + "pt par rapport à juin 2026 : 99,870" +
				nbsp + "%)",
			legend: "Disponibilité quotidienne, 1 juil. – 31 juil. (UTC) : vert ≥ 99,9" + nbsp + "%, ambre ≥ 99" + nbsp +
				"%, orange ≥ 95" + nbsp + "%, rouge en dessous.",
			badge:   "Sain",
			english: leftovers,
		},
		i18n.LangGerman: {
			subject:   "acme, Juli 2026: 99,950" + nbsp + "% Verfügbarkeit",
			preheader: "99,950" + nbsp + "% Verfügbarkeit · 3 Vorfälle · Juli 2026.",
			title:     "Verfügbarkeitsbericht",
			overall: "Gesamtverfügbarkeit: 99,950" + nbsp + "% (+0,080" + nbsp + "Prozentpunkte gegenüber Juni 2026: 99,870" +
				nbsp + "%)",
			legend: "Tägliche Verfügbarkeit, 1. Juli – 31. Juli (UTC): grün ≥ 99,9" + nbsp + "%, bernsteinfarben ≥ 99" + nbsp +
				"%, orange ≥ 95" + nbsp + "%, darunter rot.",
			badge:   "Gesund",
			english: leftovers,
		},
		i18n.LangSpanish: {
			subject:   "acme, julio de 2026: 99,950" + nbsp + "% de disponibilidad",
			preheader: "99,950" + nbsp + "% de disponibilidad · 3 incidentes · julio de 2026.", //nolint:misspell // Spanish
			title:     "Informe de disponibilidad",
			overall: "Disponibilidad global: 99,950" + nbsp + "% (+0,080" + nbsp + "puntos frente a junio de 2026: 99,870" +
				nbsp + "%)",
			legend: "Disponibilidad diaria, 1 jul – 31 jul (UTC): verde ≥ 99,9" + nbsp + "%, ámbar ≥ 99" + nbsp +
				"%, naranja ≥ 95" + nbsp + "%, rojo por debajo.",
			badge:   "Saludable",
			english: leftovers,
		},
	}
}

// TestUptimeReportRendersInEveryLanguage renders the report the way the email
// job does (the copy ForReader writes, through the JSON round trip, with the
// reader's locale) for a reader in New York reading a Paris schedule: the
// period stays the schedule's July.
func TestUptimeReportRendersInEveryLanguage(t *testing.T) {
	t.Parallel()

	formatter, err := email.NewFormatter()
	require.NoError(t, err)

	for lang, want := range localeWants() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			loc := i18n.New(lang, "America/New_York").WithClock(sampleClock)
			data := sampleData().In(loc)

			subject, html, text, err := formatter.Format(uptimereport.TemplateName, loc, roundTrip(t, data))
			r.NoError(err)

			r.Equal(want.subject, subject)
			r.Contains(html, `<html lang="`+lang+`">`)

			preheader := preheaderPattern.FindStringSubmatch(html)
			r.Len(preheader, 2)
			r.Equal(want.preheader, strings.TrimSpace(preheader[1]))

			r.Contains(html, "<h1")
			r.Contains(html, want.title)
			r.Contains(html, want.legend)
			r.Contains(html, `badge badge-ok`)
			r.Contains(html, want.badge)
			r.Contains(text, want.overall)

			for _, body := range []string{subject, html, text} {
				r.NotContains(body, "<no value>")
				r.NotContains(body, "{{")
				r.NotContains(body, "—", "no em dash in the copy")
			}

			for _, english := range want.english {
				r.NotContains(text, english, "English left in the %s text part", lang)
			}
		})
	}
}

// TestUptimeReportPluralsSurviveTheJob: counts reach the template as float64
// after the job's JSON round trip, and French puts 0 in the singular.
func TestUptimeReportPluralsSurviveTheJob(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	for _, tc := range []struct {
		lang  string
		count int
		want  string
	}{
		{i18n.LangEnglish, 0, "· 0 incidents ·"},
		{i18n.LangEnglish, 1, "· 1 incident ·"},
		{i18n.LangFrench, 0, "· 0 incident ·"},
		{i18n.LangFrench, 2, "· 2 incidents ·"},
		{i18n.LangGerman, 1, "· 1 Vorfall ·"},
		{i18n.LangSpanish, 1, "· 1 incidente ·"},
	} {
		loc := i18n.New(tc.lang, "")
		data := sampleData()
		data.IncidentCount = tc.count

		_, html, _, err := formatter.Format(uptimereport.TemplateName, loc, roundTrip(t, data.In(loc)))
		r.NoError(err)

		preheader := preheaderPattern.FindStringSubmatch(html)
		r.Len(preheader, 2)
		r.Contains(preheader[1], tc.want, "%s, %d incidents", tc.lang, tc.count)
	}
}
