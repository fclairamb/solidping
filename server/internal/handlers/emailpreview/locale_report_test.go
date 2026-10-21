package emailpreview_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPreview_UptimeReportIsWrittenInTheRequestedLanguage: the report fixture
// is written by uptimereport in the previewed language, figures included, and
// English keeps today's figures.
func TestPreview_UptimeReportIsWrittenInTheRequestedLanguage(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)

	for _, tc := range []struct {
		lang string
		html []string
		text []string
	}{
		{
			lang: "en",
			html: []string{"<html lang=\"en\">", "Uptime report", "99.950%", "+0.080 pts", "3.20 s", "Healthy"},
			text: []string{"Overall availability: 99.950%", "Incidents: 3 (longest 42m, average 21m 40s)"},
		},
		{
			lang: "fr",
			html: []string{"<html lang=\"fr\">", "Rapport de disponibilité", "99,950 %", "+0,080 pt", "Sain"},
			text: []string{"Disponibilité globale : 99,950 %", "juillet", "par rapport à juin"},
		},
		{
			lang: "de",
			html: []string{"<html lang=\"de\">", "Verfügbarkeitsbericht", "99,950 %", "3,20 s", "Gesund"},
			text: []string{"Gesamtverfügbarkeit: 99,950 %", "Juli", "gegenüber Juni"},
		},
		{
			lang: "es",
			html: []string{"<html lang=\"es\">", "Informe de disponibilidad", "99,950 %", "Saludable"},
			text: []string{"Disponibilidad global: 99,950 %", "julio de", "frente a junio de"},
		},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			base := "/api/mgmt/email-preview/uptime-report.html?locale=" + tc.lang

			html := doGet(t, router, base)
			r.Equal(http.StatusOK, html.Code, html.Body.String())

			for _, want := range tc.html {
				r.Contains(html.Body.String(), want)
			}

			text := doGet(t, router, base+"&format=text")
			r.Equal(http.StatusOK, text.Code, text.Body.String())

			for _, want := range tc.text {
				r.Contains(text.Body.String(), want)
			}
		})
	}
}
