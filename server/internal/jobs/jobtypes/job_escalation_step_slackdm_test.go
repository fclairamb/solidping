package jobtypes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestEscalationSlackDMMessage verifies the Slack DM text addresses the
// incident by check name and short #N reference — never the UID — and
// hyperlinks the dashboard mention with Slack's <url|text> syntax when a
// full incident URL can be built, degrading to plain text otherwise.
func TestEscalationSlackDMMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		incident   *models.Incident
		checkName  string
		orgSlug    string
		baseURL    string
		want       string
		wantNoLink bool // no baseURL/orgSlug -> the raw text must never carry the UID at all
	}{
		{
			name:      "numbered incident links to its detail page",
			incident:  &models.Incident{UID: "inc-1", Number: 42},
			checkName: "API prod",
			orgSlug:   "acme",
			baseURL:   "https://solidping.example.com",
			want: "[escalation] Incident #42 for API prod requires your attention. " +
				"<https://solidping.example.com/dash0/orgs/acme/incidents/inc-1|Open the dashboard> " +
				"to acknowledge or resolve.",
		},
		{
			name:      "unnumbered incident omits the reference, not the name",
			incident:  &models.Incident{UID: "inc-2"},
			checkName: "API prod",
			orgSlug:   "acme",
			baseURL:   "https://solidping.example.com",
			want: "[escalation] Incident for API prod requires your attention. " +
				"<https://solidping.example.com/dash0/orgs/acme/incidents/inc-2|Open the dashboard> " +
				"to acknowledge or resolve.",
		},
		{
			name:      "missing base URL degrades to plain text, no link",
			incident:  &models.Incident{UID: "inc-3", Number: 7},
			checkName: "API prod",
			orgSlug:   "acme",
			baseURL:   "",
			want: "[escalation] Incident #7 for API prod requires your attention. " +
				"Open the dashboard to acknowledge or resolve.",
			wantNoLink: true,
		},
		{
			name:      "missing org slug degrades to plain text, no link",
			incident:  &models.Incident{UID: "inc-4", Number: 9},
			checkName: "API prod",
			orgSlug:   "",
			baseURL:   "https://solidping.example.com",
			want: "[escalation] Incident #9 for API prod requires your attention. " +
				"Open the dashboard to acknowledge or resolve.",
			wantNoLink: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			got := escalationSlackDMMessage(tt.incident, tt.checkName, tt.orgSlug, tt.baseURL)
			r.Equal(tt.want, got)

			if tt.wantNoLink {
				// No URL is built at all in this case, so the raw UID must
				// never appear anywhere in the text — this is the exact
				// degenerate case the bug fixed here used to hit.
				r.NotContains(got, tt.incident.UID, "message text must never expose the incident UID")
			}

			r.Contains(got, "Incident", "message must address the incident by name/ref, never bare UID")
		})
	}
}
