package email

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewFormatter(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)
	r.NotNil(formatter)
}

// incidentViewModel returns the view-model shape notifications/email.go builds
// for incident-created.html (and, with minor tweaks, its siblings). Kept as a
// helper here so template-rendering tests (this file) exercise the same field
// names the real sender uses, without importing the notifications package
// (which would create an import cycle: notifications imports email).
func incidentViewModel() map[string]any {
	return map[string]any{
		"CheckName":      "Production API",
		"CheckType":      "http",
		"CheckURL":       "https://example.com/d/orgs/acme/checks/9f8e7d6c-5b4a-4c2d-9e0f-abcdef012345",
		"StartedAt":      "2026-07-05 10:00:00",
		"IncidentUID":    "inc-123",
		"IncidentNumber": int64(42),
		"IncidentURL":    "https://example.com/d/orgs/acme/incidents/inc-123",
		"AckURL":         "https://example.com/api/v1/orgs/acme/incidents/inc-123/ack?token=abc",
		"DashboardURL":   "https://example.com/d",
		"DocsURL":        "https://example.com/docs",
		"FailureCount":   3,
		"RelapseCount":   2,
		"ResolvedAt":     "2026-07-05 10:15:00",
		"Duration":       "15m0s",
		"SubjectPrefix":  "",
	}
}

// checkNameHeadlinePattern matches the headline linking the check name to
// CheckURL. A bare hostname-shaped name would get auto-linkified by mail
// clients straight to the down target instead of the check page.
const checkNameHeadlinePattern = `<h1[^>]*><a href="https://example\.com/d/orgs/acme/checks/` +
	`9f8e7d6c-5b4a-4c2d-9e0f-abcdef012345"[^>]*>Production API</a> `

// ackButtonPattern matches the primary "I'm on it" button pointing at the ack
// magic link.
const ackButtonPattern = `<a [^>]*href="https://example\.com/api/v1/orgs/acme/incidents/inc-123/ack\?token=abc"` +
	`[^>]*>I&#39;m on it</a>`

func TestFormatter_FormatIncidentCreated(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	subject, html, text, err := formatter.Format("incident-created.html", incidentViewModel())
	r.NoError(err)

	r.Equal("🔴 Production API is down (#42)", subject)

	r.Contains(html, "Production API")
	r.Contains(html, "is down")
	r.Contains(html, "https://example.com/d/orgs/acme/checks/9f8e7d6c-5b4a-4c2d-9e0f-abcdef012345")
	r.Contains(html, "https://example.com/d/orgs/acme/incidents/inc-123")
	r.Contains(html, "SolidPing")
	r.Contains(html, "style=") // CSS inlined onto elements
	r.Contains(html, "#42")
	r.Contains(html, ">Since</td>")
	r.Contains(html, `class="stripe stripe-down"`)
	r.Contains(html, `class="pill pill-down"`)

	// The redundant rows and the old ack label are gone.
	r.NotContains(html, ">Type</td>")
	r.NotContains(html, ">Check</td>")
	r.NotContains(html, ">Started at</td>")
	r.NotContains(html, "Acknowledge incident")
	r.NotContains(html, "Incident reference")
	r.NotContains(html, `class="cause"`, "no failure reason, no cause box")

	r.Regexp(checkNameHeadlinePattern+`is down</h1>`, html,
		"the headline must wrap the check name in an <a href> to CheckURL")

	// "I'm on it" is the primary action, before the secondary "View incident".
	r.Regexp(`btn-primary[^>]*>\s*<tbody><tr>\s*<td[^>]*>`+ackButtonPattern, html)
	r.Regexp(`btn-secondary[^>]*>\s*<tbody><tr>\s*<td[^>]*><a [^>]*href="https://example\.com/d/orgs/acme/`+
		`incidents/inc-123"[^>]*>View incident</a>`, html)
	r.Less(strings.Index(html, "I&#39;m on it"), strings.Index(html, "View incident"))

	r.Contains(text, "Production API is down")
	r.Contains(text, "https://example.com/d/orgs/acme/incidents/inc-123")
	r.Contains(text, "Acknowledge this incident")
	r.Contains(text, "Incident: #42")
	r.Contains(text, "Incident ID: inc-123", "the plaintext keeps the incident id")
}

// TestFormatter_FormatIncidentCreatedShowsCauseAndTarget pins the triage facts
// an on-call reader needs from the lock screen: the cause in the subject (cut
// to its first line) and in a cause box, and the probed target.
func TestFormatter_FormatIncidentCreatedShowsCauseAndTarget(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	data := incidentViewModel()
	data["FailureReason"] = "HTTP 503 Service Unavailable (expected 200)\nbody: upstream connect error"
	data["CheckTarget"] = "https://api.acme.com/health"

	subject, html, text, err := formatter.Format("incident-created.html", data)
	r.NoError(err)

	r.Equal("🔴 Production API is down: HTTP 503 Service Unavailable (expected 200) (#42)", subject)
	r.Regexp(`<div class="cause"[^>]*><span class="cause-text"[^>]*>HTTP 503 Service Unavailable \(expected 200\)`, html)
	// The target goes through nolink: plain text, never an auto-linked URL.
	r.Regexp(`>Target</td><td class="value"[^>]*>https:[^<]*acme[^<]*health</td>`, html)
	r.Contains(text, "HTTP 503 Service Unavailable (expected 200)")
	r.Contains(text, "Target: https://api.acme.com/health")
}

// TestFormatter_IncidentNumberOmittedWhenZero pins the legacy-incident
// behavior: incidents created before the per-org number backfill have
// IncidentNumber == 0, and the templates must never render a bogus "#0" —
// the whole "Incident" row/line is omitted instead.
func TestFormatter_IncidentNumberOmittedWhenZero(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	data := incidentViewModel()
	data["IncidentNumber"] = int64(0)

	subject, html, text, err := formatter.Format("incident-created.html", data)
	r.NoError(err)
	r.NotContains(subject, "#0")
	r.NotContains(subject, "#")
	// The HTML assertion is anchored on ">#0" — i.e. "#0" rendered as element
	// TEXT — rather than a bare "#0", which the branded palette's hex colors
	// (#07112a, #1e64ef, #0e8f2a) now match inside inlined style attributes.
	// The number only ever appears as the content of a <td> or an <a>, so this
	// still fails the moment a "#0" is rendered to the reader.
	r.NotContains(html, ">#0")
	r.NotContains(text, "Incident: #")
}

func TestFormatter_FormatIncidentCreatedWithoutAckURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	data := incidentViewModel()
	data["AckURL"] = ""

	_, html, text, err := formatter.Format("incident-created.html", data)
	r.NoError(err)
	r.NotContains(html, "I&#39;m on it")
	r.NotContains(html, "stops escalation", "no ack link, no explanation of it")
	r.Contains(html, "View incident")
	r.NotContains(text, "Acknowledge this incident")
}

func TestFormatter_FormatIncidentCreatedWithSubjectPrefix(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	data := incidentViewModel()
	data["SubjectPrefix"] = "[Acme]"

	subject, _, _, err := formatter.Format("incident-created.html", data)
	r.NoError(err)
	r.Equal("[Acme] 🔴 Production API is down (#42)", subject)
}

func TestFormatter_FormatIncidentResolved(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	subject, html, text, err := formatter.Format("incident-resolved.html", incidentViewModel())
	r.NoError(err)

	r.Equal("🟢 Production API is back up after 15m0s (#42)", subject)
	r.Regexp(checkNameHeadlinePattern+`is back up</h1>`, html)
	r.Contains(html, `class="pill pill-recovered"`)
	r.Contains(html, "15m0s")
	r.Contains(html, "#42")
	r.Contains(html, "View incident")
	r.NotContains(html, "I&#39;m on it", "resolved events have nothing to ack")
	r.Contains(text, "recovered")
	r.Contains(text, "15m0s")
	r.Contains(text, "Incident: #42")
}

func TestFormatter_FormatIncidentEscalated(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	subject, html, text, err := formatter.Format("incident-escalated.html", incidentViewModel())
	r.NoError(err)

	r.Equal("🟠 Production API is still down, escalated (#42)", subject)
	r.Regexp(checkNameHeadlinePattern+`is still down</h1>`, html)
	r.Contains(html, "escalated")
	r.Regexp(`>Failed checks</td><td class="value"[^>]*>3</td>`, html)
	r.Regexp(ackButtonPattern, html)
	r.Contains(html, "#42")
	r.Contains(text, "escalated")
	r.Contains(text, "Failure count: 3")
	r.Contains(text, "Incident: #42")
}

func TestFormatter_FormatIncidentReopened(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	subject, html, text, err := formatter.Format("incident-reopened.html", incidentViewModel())
	r.NoError(err)

	r.Equal("🔴 Production API is down again (#42)", subject)
	r.Regexp(checkNameHeadlinePattern+`is down again</h1>`, html)
	r.Contains(html, `class="pill pill-reopened"`)
	r.Contains(html, "(2 relapses so far)")
	r.Regexp(ackButtonPattern, html)
	r.Contains(html, "#42")
	r.Contains(text, "reopened")
	r.Contains(text, "Relapse count: 2")
	r.Contains(text, "Incident: #42")
}

// TestFormatter_FormatEscalation pins the escalation.html template at the
// formatter layer (job_escalation_step.go's sendEscalationEmail builds this
// same shape): an HTML part exists, and the incident is addressed by its
// human-facing #N reference in both subject and body, HTML and text.
func TestFormatter_FormatEscalation(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	data := map[string]any{
		"CheckName":      "Production API",
		"CheckURL":       "https://example.com/d/orgs/acme/checks/9f8e7d6c-5b4a-4c2d-9e0f-abcdef012345",
		"IncidentNumber": int64(42),
		"IncidentURL":    "https://example.com/d/orgs/acme/incidents/inc-123",
		"StartedAt":      "Jul 5, 10:00 UTC",
		"FailureCount":   3,
		"FailureReason":  "HTTP 503 Service Unavailable (expected 200)",
		"CheckTarget":    "https://api.acme.com/health",
		"AckURL":         "https://example.com/api/v1/orgs/acme/incidents/inc-123/ack?token=abc",
		"DashboardURL":   "https://example.com/d",
		"DocsURL":        "https://example.com/docs",
	}

	subject, html, text, err := formatter.Format("escalation.html", data)
	r.NoError(err)

	r.Equal("🟠 Production API is still down, escalated to you (#42)", subject)
	r.NotEmpty(html, "escalation email must have an HTML part")
	r.Contains(html, "#42")
	r.Contains(html, "style=") // CSS inlined
	r.Contains(html, "escalated to you")
	r.Contains(html, `class="cause"`)
	r.Contains(html, ">Target</td>")
	r.Regexp(ackButtonPattern, html, "the person this wakes up can ack from the mail")
	r.NotEmpty(text)
	r.Contains(text, "Incident: #42")
	r.Contains(text, "HTTP 503 Service Unavailable (expected 200)")
	r.Contains(text, "Target: https://api.acme.com/health")
	r.Contains(text, "I'm on it (acknowledge, stops escalation): "+
		"https://example.com/api/v1/orgs/acme/incidents/inc-123/ack?token=abc")

	r.Regexp(checkNameHeadlinePattern+`is still down</h1>`, html,
		"the headline must wrap the check name in an <a href> to CheckURL")
	r.Contains(text, "Check URL: https://example.com/d/orgs/acme/checks/9f8e7d6c-5b4a-4c2d-9e0f-abcdef012345",
		"the text part must carry the check URL explicitly since it cannot use an anchor")
}

func TestFormatter_UnsubscribeFooterLink(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	data := incidentViewModel()
	data["UnsubscribeURL"] = "https://example.com/unsubscribe?token=xyz"
	data["UnsubscribeCheckName"] = "Production API"

	_, html, text, err := formatter.Format("incident-created.html", data)
	r.NoError(err)
	r.Regexp(`<a href="https://example\.com/unsubscribe\?token=xyz"[^>]*>Stop alerts for Production API</a>`, html)
	r.Contains(text, "Unsubscribe from alerts for Production API")
}

func TestFormatter_InvalidTemplate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	_, html, _, err := formatter.Format("nonexistent.html", nil)
	r.Error(err)
	r.Empty(html)
	r.Contains(err.Error(), "parsing template")
}

func TestFormatter_CSSInlining(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	_, html, _, err := formatter.Format("incident-created.html", incidentViewModel())
	r.NoError(err)

	// CSS should be inlined into a style attribute on at least one element.
	r.Contains(html, "style=")
	r.Contains(html, "color")
}

// TestFormatter_TextBlockRendering pins the D1 "text" block behavior added to
// Formatter.Format: templates that define {{define "text"}} produce a
// non-empty plaintext alternative built from that block (not an
// auto-generated HTML-to-text conversion), and templates that do NOT define
// one produce exactly "" so callers fall back to HTML-only, matching
// pre-D1 behavior byte for byte.
func TestFormatter_TextBlockRendering(t *testing.T) {
	t.Parallel()

	formatter, err := NewFormatter()
	require.NoError(t, err)

	t.Run("templates with a text block produce non-empty plaintext", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			template string
			data     map[string]any
			want     []string
		}{
			{
				"registration.html",
				map[string]any{"ConfirmURL": "https://x.test/c"},
				[]string{"https://x.test/c", "Confirm your SolidPing account", "3 days"},
			},
			{
				"password-reset.html",
				map[string]any{"ResetURL": "https://x.test/r"},
				[]string{"https://x.test/r", "1 hour"},
			},
			{
				"invitation.html",
				map[string]any{
					"OrgName": "Acme", "Role": "admin", "InviterName": "Alice", "InviteURL": "https://x.test/i",
					"ExpiresIn": "24 hours",
				},
				[]string{"Acme", "admin", "Alice", "https://x.test/i", "24 hours"},
			},
			{
				"welcome.html",
				map[string]any{"DashboardURL": "https://x.test/dash"},
				[]string{"https://x.test/dash", "Welcome to SolidPing"},
			},
			{
				"password-changed.html",
				map[string]any{"ChangedAt": "Sun, 05 Jul 2026 10:00:00 UTC"},
				[]string{"Sun, 05 Jul 2026 10:00:00 UTC"},
			},
			{
				"email-changed.html",
				map[string]any{
					"ChangedAt": "Sun, 05 Jul 2026 10:00:00 UTC", "OldEmail": "alice@acme.com",
					"NewEmail": "alice@acme.org",
				},
				[]string{"Sun, 05 Jul 2026 10:00:00 UTC", "alice@acme.com", "alice@acme.org"},
			},
			{
				"membership_request_new.html",
				map[string]any{
					"OrgName": "Acme", "RequesterName": "Bob", "RequesterEmail": "bob@x.test",
					"RequestsURL": "https://x.test/requests",
				},
				[]string{"Bob", "bob@x.test", "Acme", "https://x.test/requests"},
			},
			{
				"membership_request_decision.html",
				map[string]any{
					"OrgName": "Acme", "Decision": "approved", "Role": "viewer", "DashboardURL": "https://x.test/dash",
				},
				[]string{"Acme", "approved", "viewer", "https://x.test/dash"},
			},
			{"incident-created.html", incidentViewModel(), []string{"is down", "Acknowledge this incident"}},
			{"incident-resolved.html", incidentViewModel(), []string{"recovered"}},
			{"incident-escalated.html", incidentViewModel(), []string{"escalated"}},
			{"incident-reopened.html", incidentViewModel(), []string{"reopened"}},
		}

		for _, tc := range cases {
			t.Run(tc.template, func(t *testing.T) {
				t.Parallel()

				r := require.New(t)

				_, _, text, err := formatter.Format(tc.template, tc.data)
				r.NoError(err)
				r.NotEmpty(text, "template %s must produce a non-empty text alternative", tc.template)

				for _, want := range tc.want {
					r.Contains(text, want)
				}
			})
		}
	})
}

func TestFormatter_TransactionalTemplates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		template    string
		data        map[string]any
		wantSubject string
		wantHTML    []string
	}{
		{
			// ExpiresIn is deliberately "1 hour", not the historical "7
			// days": the template used to hardcode "expires in 7 days"
			// regardless of the caller's data, so a value that disagrees
			// with that literal is the positive control — this case fails
			// against the old template and only passes once the real
			// expiresIn is rendered.
			name:     "invitation",
			template: "invitation.html",
			data: map[string]any{
				"OrgName":     "Acme",
				"Role":        "admin",
				"InviterName": "Alice",
				"InviteURL":   "https://solidping.example/i/abc",
				"ExpiresIn":   "1 hour",
			},
			wantSubject: "You're invited to join Acme on SolidPing",
			wantHTML: []string{
				"Acme",
				"admin",
				"Alice",
				"https://solidping.example/i/abc",
				"expires in 1 hour",
			},
		},
		{
			name:     "registration",
			template: "registration.html",
			data: map[string]any{
				"ConfirmURL": "https://solidping.example/c/xyz",
			},
			wantSubject: "Confirm your SolidPing account",
			wantHTML: []string{
				"https://solidping.example/c/xyz",
				"Confirm",
				"3 days",
			},
		},
		{
			name:     "password reset",
			template: "password-reset.html",
			data: map[string]any{
				"ResetURL": "https://solidping.example/r/zzz",
			},
			wantSubject: "Reset your SolidPing password",
			wantHTML: []string{
				"https://solidping.example/r/zzz",
				"1 hour",
			},
		},
		{
			name:     "welcome",
			template: "welcome.html",
			data: map[string]any{
				"DashboardURL": "https://solidping.example/dash",
			},
			wantSubject: "Welcome to SolidPing",
			wantHTML: []string{
				"https://solidping.example/dash",
			},
		},
		{
			name:     "password changed",
			template: "password-changed.html",
			data: map[string]any{
				"ChangedAt": "Sun, 05 Jul 2026 10:00:00 UTC",
			},
			wantSubject: "Your SolidPing password was changed",
			wantHTML: []string{
				"Sun, 05 Jul 2026 10:00:00 UTC",
			},
		},
		{
			name:     "membership request new",
			template: "membership_request_new.html",
			data: map[string]any{
				"OrgName":        "Acme",
				"RequesterName":  "Bob",
				"RequesterEmail": "bob@example.com",
				"RequestsURL":    "https://solidping.example/orgs/acme/requests",
			},
			wantSubject: "New membership request from Bob on Acme",
			wantHTML: []string{
				"Bob",
				"bob@example.com",
				"Acme",
				"https://solidping.example/orgs/acme/requests",
			},
		},
		{
			name:     "membership request decision approved",
			template: "membership_request_decision.html",
			data: map[string]any{
				"OrgName":      "Acme",
				"Decision":     "approved",
				"Role":         "viewer",
				"DashboardURL": "https://solidping.example/orgs/acme",
			},
			wantSubject: "Your request to join Acme was approved",
			wantHTML: []string{
				"Acme",
				"approved",
				"viewer",
				"https://solidping.example/orgs/acme",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			formatter, err := NewFormatter()
			r.NoError(err)

			subject, html, _, err := formatter.Format(tc.template, tc.data)
			r.NoError(err)
			r.Equal(tc.wantSubject, subject)

			for _, want := range tc.wantHTML {
				r.Contains(html, want)
			}
		})
	}
}

// TestFormatter_AccountMailFallbackLink pins the copy around the account-mail
// buttons: no "Hi," greeting, and a fallback paragraph that carries the raw
// link for when the button does not work.
func TestFormatter_AccountMailFallbackLink(t *testing.T) {
	t.Parallel()

	formatter, err := NewFormatter()
	require.NoError(t, err)

	cases := []struct {
		template string
		data     map[string]any
		url      string
	}{
		{"registration.html", map[string]any{"ConfirmURL": "https://x.test/c"}, "https://x.test/c"},
		{"password-reset.html", map[string]any{"ResetURL": "https://x.test/r"}, "https://x.test/r"},
	}

	for _, tc := range cases {
		t.Run(tc.template, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			_, html, _, err := formatter.Format(tc.template, tc.data)
			r.NoError(err)
			r.Regexp(`<p class="fallback"[^>]*>Button not working\? Paste this link into your browser:<br/?>`+
				`<a href="`+regexp.QuoteMeta(tc.url)+`"`, html)
			r.NotContains(html, "Or paste this link")
			r.NotContains(html, "Hi,")
		})
	}
}

// TestFormatter_InvitationExpiryIsDynamic pins the fix for the invitation
// template hardcoding "This invitation expires in 7 days" regardless of the
// actual TTL (default 24h, range 1h-1w). Renders with a 1-hour ExpiresIn —
// deliberately not "7 days" — and asserts the real value appears, in BOTH
// the HTML body and the text part, and that the old hardcoded literal is
// gone from either. This is a positive control: it fails against the
// pre-fix template, which ignored ExpiresIn entirely.
func TestFormatter_InvitationExpiryIsDynamic(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	data := map[string]any{
		"OrgName":     "Acme",
		"Role":        "admin",
		"InviterName": "Alice",
		"InviteURL":   "https://solidping.example/i/abc",
		"ExpiresIn":   "1 hour",
	}

	_, html, text, err := formatter.Format("invitation.html", data)
	r.NoError(err)

	r.Contains(html, "expires in 1 hour")
	r.NotContains(html, "7 days")

	r.Contains(text, "expires in 1 hour")
	r.NotContains(text, "7 days")
}

// unresolvedTemplateVarPattern matches a Go template action that leaked into
// rendered output unresolved (e.g. "{{.Foo}}" or "<no value>"), which would
// mean the fixture data was missing a field the template expects.
var unresolvedTemplateVarPattern = regexp.MustCompile(`\{\{|<no value>`)

// bodyStyleBlockPattern matches a <style> tag that survived inside <body>.
// premailer inlines every selector it can turn into a style="" attribute,
// but @media rules are inherently conditional and cannot be inlined — those
// are left in a single <style> block in <head> (standard practice for
// responsive HTML email, degrading gracefully when a client strips <head>
// styles). The acceptance criterion is "zero <style> blocks remaining in
// the body", so this checks specifically inside <body>...</body>, not the
// whole document.
var bodyStyleBlockPattern = regexp.MustCompile(`(?s)<body[^>]*>.*<style`)

// TestFormatter_AllShippedTemplatesRenderCleanly is the acceptance-criterion-2
// golden/smoke test: every shipped template renders with fixture data,
// producing valid-looking HTML with zero remaining <style> blocks in the
// body (i.e. fully inlined by premailer — only the un-inlinable @media block
// may remain, and only in <head>) and no unresolved template variables.
func TestFormatter_AllShippedTemplatesRenderCleanly(t *testing.T) {
	t.Parallel()

	incidentData := incidentViewModel()

	cases := []struct {
		template string
		data     map[string]any
	}{
		{"incident-created.html", incidentData},
		{"incident-resolved.html", incidentData},
		{"incident-escalated.html", incidentData},
		{"incident-reopened.html", incidentData},
		{"registration.html", map[string]any{"ConfirmURL": "https://x.test/c"}},
		{"password-reset.html", map[string]any{"ResetURL": "https://x.test/r"}},
		{"password-reset-sso.html", map[string]any{"ResetURL": "https://x.test/r", "Providers": "Google or GitHub"}},
		{"invitation.html", map[string]any{
			"OrgName": "Acme", "Role": "admin", "InviterName": "Alice", "InviteURL": "https://x.test/i",
			"ExpiresIn": "24 hours",
		}},
		{"welcome.html", map[string]any{"DashboardURL": "https://x.test/dash"}},
		{"password-changed.html", map[string]any{"ChangedAt": "Sun, 05 Jul 2026 10:00:00 UTC"}},
		{"email-changed.html", map[string]any{
			"ChangedAt": "Sun, 05 Jul 2026 10:00:00 UTC", "OldEmail": "alice@acme.com", "NewEmail": "alice@acme.org",
		}},
		{"membership_request_new.html", map[string]any{
			"OrgName": "Acme", "RequesterName": "Bob", "RequesterEmail": "bob@x.test",
			"RequestsURL": "https://x.test/requests",
		}},
		{"membership_request_decision.html", map[string]any{
			"OrgName": "Acme", "Decision": "approved", "Role": "viewer", "DashboardURL": "https://x.test/dash",
		}},
	}

	formatter, err := NewFormatter()
	require.NoError(t, err)

	for _, tc := range cases {
		t.Run(tc.template, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			subject, html, text, err := formatter.Format(tc.template, tc.data)
			r.NoError(err, "template %s must render without error", tc.template)
			r.NotEmpty(html, "template %s must produce non-empty HTML", tc.template)

			r.False(bodyStyleBlockPattern.MatchString(html),
				"template %s must have CSS fully inlined by premailer (no remaining <style> block in <body>)", tc.template)
			r.False(unresolvedTemplateVarPattern.MatchString(html),
				"template %s HTML must not leak unresolved template syntax", tc.template)
			r.False(unresolvedTemplateVarPattern.MatchString(subject),
				"template %s subject must not leak unresolved template syntax", tc.template)

			if text != "" {
				r.False(unresolvedTemplateVarPattern.MatchString(text),
					"template %s text must not leak unresolved template syntax", tc.template)
			}
		})
	}
}

func TestShorten(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		max  int
		in   any
		want string
	}{
		{"short value kept", 60, "HTTP 503", "HTTP 503"},
		{"trimmed", 60, "  HTTP 503  ", "HTTP 503"},
		{"first line only", 60, "HTTP 503\nbody: upstream error", "HTTP 503"},
		{"exactly at the cap", 5, "abcde", "abcde"},
		{"cut with ellipsis", 5, "abcdef", "abcd…"},
		{"counts runes, not bytes", 4, "ééééé", "ééé…"},
		{"no trailing space before the ellipsis", 6, "abcd efgh", "abcd…"},
		{"nil", 60, nil, ""},
		{"non-string", 60, 42, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, shorten(tc.max, tc.in))
		})
	}
}

func TestDict(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	m, err := dict("a", 1, "b", "two")
	r.NoError(err)
	r.Equal(map[string]any{"a": 1, "b": "two"}, m)

	_, err = dict("a", 1, "b")
	r.ErrorIs(err, errOddDictArgs)

	_, err = dict(1, "a")
	r.ErrorIs(err, errDictKeyNotString)
}

// TestSubjectAndTextAreNotHTMLEscaped pins the split between the three parts
// Format returns: only the HTML body is HTML.
//
// The whole template set is parsed by html/template, whose escaper rewrites
// every INTERPOLATED value (literal template text is left alone, which is why
// this stayed invisible for so long). A check named "Search & Discovery"
// therefore reached the reader as "Search &amp; Discovery" — in the subject
// line, before they even opened the mail, and again in the plaintext part.
// Subject and text now render through text/template.
//
// The last assertion is the one that matters most: the HTML body must STILL be
// escaped. Rendering everything as text would fix the subject and quietly turn
// every user-controlled name in the HTML part into markup injection.
func TestSubjectAndTextAreNotHTMLEscaped(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	const hostile = `Search & Discovery "prod" <b>`

	viewModel := incidentViewModel()
	viewModel["CheckName"] = hostile

	subject, htmlBody, text, err := formatter.Format("incident-created.html", viewModel)
	r.NoError(err)

	r.Contains(subject, hostile, "the subject line is a mail header, not HTML")
	r.NotContains(subject, "&amp;")
	r.Contains(text, hostile, "the plaintext alternative is not HTML either")
	r.NotContains(text, "&amp;")

	r.NotContains(htmlBody, "<b>", "the HTML body must keep escaping interpolated values")
	r.Contains(htmlBody, "&amp;")
}

// TestRegionOfflineAgreesWithCount pins the verb on the count: one blind check
// "is not running", never "1 of your checks are not running".
func TestRegionOfflineAgreesWithCount(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	for count, want := range map[int][2]string{
		1: {"1 of your checks is not running", "runs only from this region, so it is"},
		2: {"2 of your checks are not running", "run only from this region, so they are"},
	} {
		subject, _, text, err := formatter.Format("region-offline.html", map[string]any{
			"RegionName": "Tokyo (tokyo)",
			"Since":      "2026-10-16 17:58 UTC",
			"OrgName":    "acme",
			"BlindCount": count,
		})
		r.NoError(err)
		r.Equal("🔴 Region Tokyo (tokyo) is offline: "+want[0], subject)
		r.Contains(text, want[1])
	}
}
