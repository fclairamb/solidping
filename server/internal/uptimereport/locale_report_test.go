package uptimereport_test

import (
	"html"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/slo"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
)

// nbsp is the no-break space fr, de and es put between a number and its unit.
const nbsp = " "

// preheaderPattern reads the hidden preheader base.html puts first in the
// body, up to the zero-width padding that follows it.
var preheaderPattern = regexp.MustCompile(`(?s)display:none[^>]*>\s*(.*?)\s*(?:\x{200B}|&#8203;)`)

var (
	// invisiblePattern is markup a reader never sees: the head (styles) and
	// any remaining style block.
	invisiblePattern = regexp.MustCompile(`(?is)<head\b.*?</head>|<style\b.*?</style>`)
	tagPattern       = regexp.MustCompile(`(?s)<[^>]*>`)
	spacesPattern    = regexp.MustCompile(`\s+`)
)

// visibleText is what a reader of the HTML part sees: no markup, no
// attributes (URLs, classes), entities decoded.
func visibleText(body string) string {
	body = invisiblePattern.ReplaceAllString(body, " ")
	body = tagPattern.ReplaceAllString(body, " ")

	return spacesPattern.ReplaceAllString(html.UnescapeString(body), " ")
}

// englishLeftovers is copy that must not survive in a translated render.
// Each phrase is checked in the parts (subject, HTML, text) where the English
// render actually has it: that render is the positive control.
func englishLeftovers() []string {
	return []string{
		"Uptime report", "Availability", "Response time", "Objectives", "Open dashboard",
		"worst first", "No data", "Checks monitored", "Total downtime", "Budget remaining",
		" vs ", "uptime", "longest", "average", "samples", "Gray means",
	}
}

// renderReport renders the report the way the email job does: the copy
// written for loc, through the JSON round trip, with loc.
func renderReport(
	t *testing.T, formatter *email.TemplateFormatter, loc i18n.Locale, data *uptimereport.Data,
) (string, string, string) {
	t.Helper()

	subject, body, text, err := formatter.Format(uptimereport.TemplateName, loc, roundTrip(t, data.In(loc)))
	require.NoError(t, err)

	return subject, body, text
}

// readerParts is what a reader reads of a mail, by part: the subject, the
// visible text of the HTML part, and the text part.
func readerParts(subject, body, text string) map[string]string {
	return map[string]string{"subject": subject, "html": visibleText(body), "text": text}
}

type localeWant struct {
	subject, preheader, title, overall, legend, badge string
	// translated: English copy must not survive in this language's render.
	translated bool
}

func localeWants() map[string]localeWant {
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
			preheader: "99,950" + nbsp + "% de disponibilité · 3 incidents · juillet 2026",
			title:     "Rapport de disponibilité",
			overall: "Disponibilité globale : 99,950" + nbsp + "% (+0,080" + nbsp + "pt par rapport à juin 2026 : 99,870" +
				nbsp + "%)",
			legend: "Disponibilité quotidienne, 1 juil. – 31 juil. (UTC) : vert ≥ 99,9" + nbsp + "%, ambre ≥ 99" + nbsp +
				"%, orange ≥ 95" + nbsp + "%, rouge en dessous.",
			badge:      "Sain",
			translated: true,
		},
		i18n.LangGerman: {
			subject:   "acme, Juli 2026: 99,950" + nbsp + "% Verfügbarkeit",
			preheader: "99,950" + nbsp + "% Verfügbarkeit · 3 Vorfälle · Juli 2026",
			title:     "Verfügbarkeitsbericht",
			overall: "Gesamtverfügbarkeit: 99,950" + nbsp + "% (+0,080" + nbsp + "Prozentpunkte gegenüber Juni 2026: 99,870" +
				nbsp + "%)",
			legend: "Tägliche Verfügbarkeit, 1. Juli – 31. Juli (UTC): grün ≥ 99,9" + nbsp + "%, bernsteinfarben ≥ 99" + nbsp +
				"%, orange ≥ 95" + nbsp + "%, darunter rot.",
			badge:      "Gesund",
			translated: true,
		},
		i18n.LangSpanish: {
			subject:   "acme, julio de 2026: 99,950" + nbsp + "% de disponibilidad",
			preheader: "99,950" + nbsp + "% de disponibilidad · 3 incidentes · julio de 2026", //nolint:misspell // Spanish
			title:     "Informe de disponibilidad",
			overall: "Disponibilidad global: 99,950" + nbsp + "% (+0,080" + nbsp + "puntos frente a junio de 2026: 99,870" +
				nbsp + "%)",
			legend: "Disponibilidad diaria, 1 jul – 31 jul (UTC): verde ≥ 99,9" + nbsp + "%, ámbar ≥ 99" + nbsp +
				"%, naranja ≥ 95" + nbsp + "%, rojo por debajo.",
			badge:      "Saludable",
			translated: true,
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

	// The positive control of the leftover check: each phrase is in the
	// English render, and is looked for in the same part of the others.
	english := readerParts(renderReport(t, formatter,
		i18n.New(i18n.LangEnglish, "America/New_York").WithClock(sampleClock), sampleData()))

	for _, phrase := range englishLeftovers() {
		found := false
		for _, part := range english {
			found = found || strings.Contains(part, phrase)
		}

		require.True(t, found, "%q is in no part of the English render: the check could not fail", phrase)
	}

	for lang, want := range localeWants() {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			loc := i18n.New(lang, "America/New_York").WithClock(sampleClock)
			subject, body, text := renderReport(t, formatter, loc, sampleData())

			r.Equal(want.subject, subject)
			r.Contains(body, `<html lang="`+lang+`">`)

			preheader := preheaderPattern.FindStringSubmatch(body)
			r.Len(preheader, 2)
			r.Equal(want.preheader, strings.TrimSpace(preheader[1]))

			r.Contains(body, "<h1")
			r.Contains(body, want.title)
			r.Contains(body, want.legend)
			r.Contains(body, `badge badge-ok`)
			r.Contains(body, want.badge)
			r.Contains(text, want.overall)

			for _, part := range []string{subject, body, text} {
				r.NotContains(part, "<no value>")
				r.NotContains(part, "{{")
				r.NotContains(part, "—", "no em dash in the copy")
			}

			if !want.translated {
				return
			}

			read := readerParts(subject, body, text)
			for _, phrase := range englishLeftovers() {
				for name, part := range read {
					if strings.Contains(english[name], phrase) {
						r.NotContains(part, phrase, "English left in the %s %s part", lang, name)
					}
				}
			}
		})
	}
}

// TestUptimeReportWeeklyReadsInEveryLanguage: a weekly period is a day range
// whose short months end with a dot in fr and de ("11 oct.", "11. Okt."), so
// the preheader must not add its own, and the previous week must read after
// "vs" ("par rapport à la semaine du 28 sept. au 4 oct.").
func TestUptimeReportWeeklyReadsInEveryLanguage(t *testing.T) {
	t.Parallel()

	formatter, err := email.NewFormatter()
	require.NoError(t, err)

	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)

	monday := time.Date(2026, time.October, 5, 0, 0, 0, 0, paris)

	data := sampleData()
	data.Figures.Weekly = true
	data.Figures.Period = slo.Window{Start: monday, End: monday.AddDate(0, 0, 7)}
	data.Figures.Previous = slo.Window{Start: monday.AddDate(0, 0, -7), End: monday}

	for _, tc := range []struct {
		lang, preheader, overall string
	}{
		{
			i18n.LangEnglish,
			"99.950% availability · 3 incidents · Oct 5 – Oct 11.",
			"Overall availability: 99.950% (+0.080 pts vs Sep 28 – Oct 4: 99.870%)",
		},
		{
			i18n.LangFrench,
			"99,950" + nbsp + "% de disponibilité · 3 incidents · 5 oct. – 11 oct.",
			"Disponibilité globale : 99,950" + nbsp + "% (+0,080" + nbsp +
				"pt par rapport à la semaine du 28 sept. au 4 oct. : 99,870" + nbsp + "%)",
		},
		{
			i18n.LangGerman,
			"99,950" + nbsp + "% Verfügbarkeit · 3 Vorfälle · 5. Okt. – 11. Okt.",
			"Gesamtverfügbarkeit: 99,950" + nbsp + "% (+0,080" + nbsp +
				"Prozentpunkte gegenüber der Woche vom 28. Sept. bis 4. Okt.: 99,870" + nbsp + "%)",
		},
		{
			i18n.LangSpanish,
			"99,950" + nbsp + "% de disponibilidad · 3 incidentes · 5 oct – 11 oct", //nolint:misspell // Spanish
			"Disponibilidad global: 99,950" + nbsp + "% (+0,080" + nbsp +
				"puntos frente a la semana del 28 sept al 4 oct: 99,870" + nbsp + "%)",
		},
	} {
		loc := i18n.New(tc.lang, "America/New_York").WithClock(sampleClock)
		_, body, text := renderReport(t, formatter, loc, data)

		preheader := preheaderPattern.FindStringSubmatch(body)
		require.Len(t, preheader, 2)
		require.Equal(t, tc.preheader, strings.TrimSpace(preheader[1]), tc.lang)
		require.Contains(t, text, tc.overall, tc.lang)
		require.NotContains(t, visibleText(body), "..", tc.lang)
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

		_, body, _ := renderReport(t, formatter, loc, data)

		preheader := preheaderPattern.FindStringSubmatch(body)
		r.Len(preheader, 2)
		r.Contains(preheader[1], tc.want, "%s, %d incidents", tc.lang, tc.count)
	}
}
