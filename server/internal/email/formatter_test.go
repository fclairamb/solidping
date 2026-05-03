package email

import (
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

func TestFormatter_FormatIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	data := map[string]any{
		"CheckName":    "Production API",
		"Status":       "down",
		"Message":      "The server is not responding to health checks.",
		"DashboardURL": "https://example.com/dashboard/checks/prod-api",
	}

	subject, html, err := formatter.Format("incident.html", data)
	r.NoError(err)

	// incident.html does not yet define a subject block — should be empty.
	r.Empty(subject)

	// Check HTML content
	r.Contains(html, "Production API")
	r.Contains(html, "DOWN") // upper filter applied
	r.Contains(html, "The server is not responding to health checks.")
	r.Contains(html, "https://example.com/dashboard/checks/prod-api")
	r.Contains(html, "SolidPing")

	// Check CSS is inlined (style attribute should be present)
	r.Contains(html, "style=")
}

func TestFormatter_FormatIncidentWithoutDashboardURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	data := map[string]any{
		"CheckName": "Test Check",
		"Status":    "up",
		"Message":   "The check is now back online.",
	}

	_, html, err := formatter.Format("incident.html", data)
	r.NoError(err)

	// Check HTML content
	r.Contains(html, "Test Check")
	r.Contains(html, "UP")
	r.Contains(html, "The check is now back online.")
	// Should not contain the button since DashboardURL is not set
	r.NotContains(html, "View Dashboard")
}

func TestFormatter_InvalidTemplate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	_, html, err := formatter.Format("nonexistent.html", nil)
	r.Error(err)
	r.Empty(html)
	r.Contains(err.Error(), "parsing template")
}

func TestFormatter_CSSInlining(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	data := map[string]any{
		"CheckName":    "CSS Test",
		"Status":       "down",
		"Message":      "Testing CSS inlining.",
		"DashboardURL": "https://example.com",
	}

	_, html, err := formatter.Format("incident.html", data)
	r.NoError(err)

	// CSS should be inlined into a style attribute on at least one element.
	r.Contains(html, "style=")
	r.Contains(html, "color")
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			formatter, err := NewFormatter()
			r.NoError(err)

			subject, html, err := formatter.Format(tc.template, tc.data)
			r.NoError(err)
			r.Equal(tc.wantSubject, subject)

			for _, want := range tc.wantHTML {
				r.Contains(html, want)
			}
		})
	}
}
