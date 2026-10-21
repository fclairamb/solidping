package email

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/i18n"
)

// renderTextPartial executes one partial of base.html the way a "text" block
// calls it (text/template, the render's locale bound).
func renderTextPartial(t *testing.T, loc i18n.Locale, name string, data any) string {
	t.Helper()

	formatter, err := NewFormatter()
	require.NoError(t, err)

	tmpl, err := formatter.parseTextTemplate("test-email.html", formatter.funcsFor(loc))
	require.NoError(t, err)

	var out strings.Builder
	require.NoError(t, tmpl.ExecuteTemplate(&out, name, data))

	return out.String()
}

func TestIncidentActionsText(t *testing.T) {
	t.Parallel()

	data := map[string]any{
		"AckURL":       "https://acme.com/ack",
		"IncidentURL":  "https://acme.com/incident",
		"AckExpiresAt": time.Date(2020, 7, 12, 10, 0, 0, 0, time.UTC),
	}

	t.Run("english", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, "\nI'm on it: https://acme.com/ack\n"+
			"\"I'm on it\" tells your team you are handling it and stops escalation. "+
			"The link works until Jul 12, 2020, 10:00 UTC.\n"+
			"\nView incident: https://acme.com/incident\n",
			renderTextPartial(t, i18n.English, "incident-actions-text", data))
	})

	t.Run("french, reader's zone", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		text := renderTextPartial(t, i18n.New(i18n.LangFrench, "Europe/Paris"), "incident-actions-text", data)
		r.Contains(text, "Je m'en occupe : https://acme.com/ack\n")
		r.Contains(text, "Le lien est valable jusqu'au 12 juil. 2020, 12:00 CEST.")
		r.Contains(text, "Voir l'incident : https://acme.com/incident\n")
	})

	t.Run("each part is optional", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		viewOnly := map[string]any{"IncidentURL": "https://acme.com/incident"}
		r.Equal("\nView incident: https://acme.com/incident\n",
			renderTextPartial(t, i18n.English, "incident-actions-text", viewOnly))
		r.Equal("\nI'm on it: https://acme.com/ack\n"+
			"\"I'm on it\" tells your team you are handling it and stops escalation.\n",
			renderTextPartial(t, i18n.English, "incident-actions-text", map[string]any{"AckURL": "https://acme.com/ack"}))
		r.Empty(renderTextPartial(t, i18n.English, "incident-actions-text", map[string]any{}))
	})
}

func TestFooterText(t *testing.T) {
	t.Parallel()

	data := map[string]any{
		"DashboardURL":         "https://acme.com/d",
		"DocsURL":              "https://acme.com/docs",
		"UnsubscribeURL":       "https://acme.com/u",
		"UnsubscribeCheckName": "Search & Discovery",
	}

	t.Run("english", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, "Dashboard: https://acme.com/d\n"+
			"Docs: https://acme.com/docs\n"+
			"Stop alerts for Search & Discovery: https://acme.com/u\n",
			renderTextPartial(t, i18n.English, "footer-text", data))
	})

	t.Run("german", func(t *testing.T) {
		t.Parallel()

		text := renderTextPartial(t, i18n.New(i18n.LangGerman, ""), "footer-text", data)
		require.Contains(t, text, "Dokumentation: https://acme.com/docs\n")
	})

	t.Run("plain unsubscribe, nothing else", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		r.Equal("Unsubscribe: https://acme.com/u\n",
			renderTextPartial(t, i18n.English, "footer-text", map[string]any{"UnsubscribeURL": "https://acme.com/u"}))
		r.Empty(renderTextPartial(t, i18n.English, "footer-text", struct{ Other string }{}),
			"a struct view model without the fields")
	})
}

func TestZoneAnchoredFuncs(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	funcs := formatter.funcsFor(i18n.New(i18n.LangEnglish, "America/New_York"))
	dayIn, ok := funcs["dayIn"].(func(string, any) string)
	r.True(ok)
	monthIn, ok := funcs["monthIn"].(func(string, any) string)
	r.True(ok)

	// What a time.Time becomes after the email job's JSON round trip.
	october := "2026-10-01T00:00:00+02:00"
	r.Equal("October 2026", monthIn("Europe/Paris", october))
	r.Contains(dayIn("UTC", october), "Sep 30")
}
