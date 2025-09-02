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

	html, text, err := formatter.Format("incident.html", data)
	r.NoError(err)

	// Check HTML content
	r.Contains(html, "Production API")
	r.Contains(html, "DOWN") // upper filter applied
	r.Contains(html, "The server is not responding to health checks.")
	r.Contains(html, "https://example.com/dashboard/checks/prod-api")
	r.Contains(html, "SolidPing")

	// Check CSS is inlined (style attribute should be present)
	r.Contains(html, "style=")

	// Check plain text content
	r.NotEmpty(text)
	r.Contains(text, "Production API")
	r.Contains(text, "DOWN")
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

	html, text, err := formatter.Format("incident.html", data)
	r.NoError(err)

	// Check HTML content
	r.Contains(html, "Test Check")
	r.Contains(html, "UP")
	r.Contains(html, "The check is now back online.")
	// Should not contain the button since DashboardURL is not set
	r.NotContains(html, "View Dashboard")

	// Check plain text content
	r.NotEmpty(text)
}

func TestFormatter_InvalidTemplate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter()
	r.NoError(err)

	_, _, err = formatter.Format("nonexistent.html", nil)
	r.Error(err)
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

	html, _, err := formatter.Format("incident.html", data)
	r.NoError(err)

	// The status-down class should have its style inlined
	// The premailer should convert .status-down { color: #dc3545; } to inline style
	r.Contains(html, "color")
	r.Contains(html, "#dc3545")
}
