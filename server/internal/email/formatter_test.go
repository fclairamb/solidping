package email

import (
	"regexp"
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
		"CheckURL":       "https://example.com/dash0/orgs/acme/checks/prod-api",
		"StartedAt":      "2026-07-05 10:00:00",
		"IncidentUID":    "inc-123",
		"IncidentNumber": int64(42),
		"IncidentURL":    "https://example.com/dash0/orgs/acme/incidents/inc-123",
		"AckURL":         "https://example.com/api/v1/orgs/acme/incidents/inc-123/ack?token=abc",
		"DashboardURL":   "https://example.com/dash0",
		"DocsURL":        "https://example.com/docs",
		"FailureCount":   3,
		"RelapseCount":   2,
		"ResolvedAt":     "2026-07-05 10:15:00",
		"Duration":       "15m0s",
		"SubjectPrefix":  "",
	}
}

func TestFormatter_FormatIncidentCreated(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	subject, html, text, err := formatter.Format("incident-created.html", incidentViewModel())
	r.NoError(err)

	r.Equal("[DOWN] Production API is down (#42)", subject)

	r.Contains(html, "Production API")
	r.Contains(html, "is down")
	r.Contains(html, "http")
	r.Contains(html, "https://example.com/dash0/orgs/acme/checks/prod-api")
	r.Contains(html, "https://example.com/dash0/orgs/acme/incidents/inc-123")
	r.Contains(html, "View incident")
	r.Contains(html, "Acknowledge incident")
	r.Contains(html, "SolidPing")
	r.Contains(html, "style=") // CSS inlined onto elements
	r.Contains(html, "#42")

	r.Contains(text, "Production API is down")
	r.Contains(text, "https://example.com/dash0/orgs/acme/incidents/inc-123")
	r.Contains(text, "Acknowledge this incident")
	r.Contains(text, "Incident: #42")
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
	r.NotContains(html, "#0")
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
	r.NotContains(html, "Acknowledge incident")
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
	r.Equal("[Acme] [DOWN] Production API is down (#42)", subject)
}

func TestFormatter_FormatIncidentResolved(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	subject, html, text, err := formatter.Format("incident-resolved.html", incidentViewModel())
	r.NoError(err)

	r.Equal("[RECOVERED] Production API is back up (#42)", subject)
	r.Contains(html, "recovered")
	r.Contains(html, "15m0s")
	r.Contains(html, "#42")
	r.NotContains(html, "Acknowledge incident", "resolved events have nothing to ack")
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

	r.Equal("[ESCALATED] Production API incident escalated (#42)", subject)
	r.Contains(html, "escalated")
	r.Contains(html, "Failure count")
	r.Contains(html, "3")
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

	r.Equal("[REOPENED] Production API incident reopened (relapse #2) (#42)", subject)
	r.Contains(html, "reopened")
	r.Contains(html, "Relapse count")
	r.Contains(html, "#42")
	r.Contains(text, "reopened")
	r.Contains(text, "Relapse count: 2")
	r.Contains(text, "Incident: #42")
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
	r.Contains(html, "Unsubscribe from alerts for Production API")
	r.Contains(html, "https://example.com/unsubscribe?token=xyz")
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
				},
				[]string{"Acme", "admin", "Alice", "https://x.test/i", "7 days"},
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
			name:     "invitation",
			template: "invitation.html",
			data: map[string]any{
				"OrgName":     "Acme",
				"Role":        "admin",
				"InviterName": "Alice",
				"InviteURL":   "https://solidping.example/i/abc",
			},
			wantSubject: "You're invited to join Acme on SolidPing",
			wantHTML: []string{
				"Acme",
				"admin",
				"Alice",
				"https://solidping.example/i/abc",
				"expires in 7 days",
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
		{"invitation.html", map[string]any{
			"OrgName": "Acme", "Role": "admin", "InviterName": "Alice", "InviteURL": "https://x.test/i",
		}},
		{"welcome.html", map[string]any{"DashboardURL": "https://x.test/dash"}},
		{"password-changed.html", map[string]any{"ChangedAt": "Sun, 05 Jul 2026 10:00:00 UTC"}},
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
