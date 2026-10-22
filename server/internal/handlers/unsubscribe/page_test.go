package unsubscribe

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/i18n"
)

func TestAcceptedLanguage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		header string
		want   string
	}{
		{header: "", want: ""},
		{header: "fr", want: "fr"},
		{header: "fr-CA,fr;q=0.9,en;q=0.8", want: "fr"},
		{header: "de-CH", want: "de"},
		{header: "es-MX, en;q=0.5", want: "es"},
		{header: "en-US,en;q=0.9,fr;q=0.8", want: "en"},
		{header: "it-IT, de;q=0.5", want: "de"},    // the first supported one
		{header: "de;q=0.4, es;q=0.9", want: "es"}, // by weight, not order
		{header: "fr;q=0, de", want: "de"},         // q=0 means "not this one"
		{header: "it-IT, pt-BR;q=0.8", want: ""},
		{header: "*", want: ""},
		{header: "not a tag;;", want: ""},
	}

	for _, tc := range cases {
		require.Equal(t, tc.want, acceptedLanguage(tc.header), "Accept-Language %q", tc.header)
	}
}

// TestPages_EnglishUnchanged pins the English pages to the bytes they had
// before they were localized.
func TestPages_EnglishUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	loc := i18n.English

	r.Equal(`<h1>Unsubscribe from SolidPing alerts</h1>`+
		`<p>You are unsubscribing <strong>a&amp;b@example.com</strong> from incident alert emails.</p>`+
		`<p><a class="btn btn-primary" href="/unsubscribe?token=tok&scope=check">Unsubscribe from &lt;API&gt; only</a></p>`+
		`<p><a class="btn btn-secondary" href="/unsubscribe?token=tok&scope=org">`+
		`Unsubscribe from all alert emails for this organization</a></p>`+
		`<p class="meta">You can undo this at any time from the confirmation page shown after you unsubscribe, `+
		`as long as this link hasn't expired.</p>`,
		choiceBody(loc, "tok", "a&b@example.com", "check-uid", "<API>"))

	res := &Result{Email: "a@example.com", CheckUID: "c1", CheckName: "API", SuppressionUID: "s1"}
	r.Equal(`<h1>Unsubscribed</h1>`+
		`<p><strong>a@example.com</strong> will no longer receive alerts for API.</p>`+
		`<p>Other recipients of these alerts are not affected.</p>`+
		`<p class="meta">Changed your mind? <a href="/unsubscribe/undo?token=tok&uid=s1">Undo and re-subscribe</a></p>`,
		successBody(loc, "tok", res))

	r.Contains(successBody(loc, "tok", &Result{Email: "a@example.com", CheckUID: "c1"}),
		`<p><strong>a@example.com</strong> will no longer receive alerts for this check.</p>`)
	r.Contains(successBody(loc, "tok", &Result{Email: "a@example.com"}),
		`<p><strong>a@example.com</strong> will no longer receive all alert emails for this organization.</p>`)

	r.Equal(`<h1>Re-subscribed</h1><p>You will receive alert emails again for this scope.</p>`, resubscribedBody(loc))
	r.Equal(`<h1>Missing link parameters</h1><p>This URL is missing required parameters. `+
		`Please use the full link from the original email.</p>`, missingTokenBody(loc))
	r.Equal(`<h1>This link is invalid or has expired</h1><p>The unsubscribe link could not be verified, `+
		`or it has expired. If you have a more recent alert email, try the link from that message instead.</p>`,
		invalidTokenBody(loc))
	r.Equal(`<h1>Organization not found</h1><p>The organization this link refers to no longer exists.</p>`,
		orgNotFoundBody(loc))

	r.Contains(renderPage(loc, "Unsubscribed", ""),
		`<html lang="en"><head><meta charset="utf-8"><title>Unsubscribed</title>`)
}

// TestPages_HostileValuesAreEscaped pins the escaping CodeQL's go/reflected-xss
// cannot see through loc.THTML: every request-derived value (email, check name,
// token, suppression uid) reaches the page escaped, in every language.
func TestPages_HostileValuesAreEscaped(t *testing.T) {
	t.Parallel()

	const evil = `<script>alert(1)</script>"'`

	for _, lang := range i18n.Supported() {
		loc := i18n.New(lang, "")

		pages := map[string]string{
			"choice": choiceBody(loc, evil, evil, evil, evil),
			"success": successBody(loc, evil, &Result{
				Email: evil, CheckUID: evil, CheckName: evil, SuppressionUID: evil,
			}),
			"document": renderPage(loc, evil, "<p>x</p>"),
		}

		for name, page := range pages {
			require.NotContains(t, page, "<script", "%s page in %s", name, lang)
			require.NotContains(t, page, `"'`, "%s page in %s", name, lang)
		}
	}
}
