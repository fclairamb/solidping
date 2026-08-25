package notifications

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const brandingIncidentBaseURL = "https://solidping.example"

// brandingJobContext wires a formatter that knows the base URL, which is what
// makes the logo <img> absolute — exactly as internal/app does at boot.
func brandingJobContext(t *testing.T, sender *fakeEmailSender) *jobdef.JobContext {
	t.Helper()

	formatter, err := email.NewFormatter(email.WithBaseURL(brandingIncidentBaseURL))
	require.NoError(t, err)

	return &jobdef.JobContext{
		Services:  &services.Registry{EmailSender: sender, EmailFormatter: formatter},
		AppConfig: &config.Config{Auth: config.AuthConfig{JWTSecret: "test-secret"}},
		Logger:    slog.Default(),
	}
}

// TestIncidentEmailCarriesOrgBranding pins stage 3's org threading: the org's
// name and logo, resolved once by the notification job runner and carried on
// the payload, reach the rendered email.
func TestIncidentEmailCarriesOrgBranding(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	checkName := "API health"
	sender := &fakeEmailSender{}
	emailSender := &EmailSender{}

	payload := &Payload{
		EventType:  eventTypeIncidentResolved,
		Check:      &models.Check{UID: "check-uid", Name: &checkName, Type: "http"},
		Incident:   &models.Incident{UID: "incident-uid", CheckUID: "check-uid"},
		OrgSlug:    "acme",
		OrgName:    "Acme Corp",
		OrgLogoURL: "/pub/assets/org-logo-uid",
		AppBaseURL: brandingIncidentBaseURL,
		Integration: &models.Integration{
			OrganizationUID: "org-uid",
			Settings:        models.JSONMap{"to": []any{"ops@example.com"}},
		},
	}

	jctx := brandingJobContext(t, sender)

	r.NoError(emailSender.Send(context.Background(), jctx, payload))
	r.Len(sender.sent, 1)

	html := sender.sent[0].HTML
	r.Contains(html, brandingIncidentBaseURL+"/pub/assets/org-logo-uid")
	r.Contains(html, `alt="Acme Corp"`)
	r.Contains(html, "Acme Corp — sent by SolidPing")
	// The org logo is the primary mark, so the product logo is not also shown.
	r.NotContains(html, "/dash0/logo.png")
}

// TestIncidentEmailFallsBackToTheProductLogo is the negative control: an org
// with no logo of its own gets the SolidPing logo, not a broken image and not
// nothing.
func TestIncidentEmailFallsBackToTheProductLogo(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	checkName := "API health"
	sender := &fakeEmailSender{}
	emailSender := &EmailSender{}

	payload := &Payload{
		EventType:  eventTypeIncidentResolved,
		Check:      &models.Check{UID: "check-uid", Name: &checkName, Type: "http"},
		Incident:   &models.Incident{UID: "incident-uid", CheckUID: "check-uid"},
		OrgSlug:    "acme",
		OrgName:    "Acme Corp",
		AppBaseURL: brandingIncidentBaseURL,
		Integration: &models.Integration{
			OrganizationUID: "org-uid",
			Settings:        models.JSONMap{"to": []any{"ops@example.com"}},
		},
	}

	r.NoError(emailSender.Send(context.Background(), brandingJobContext(t, sender), payload))
	r.Len(sender.sent, 1)

	html := sender.sent[0].HTML
	r.Contains(html, brandingIncidentBaseURL+"/dash0/logo.png")
	r.Contains(html, `alt="SolidPing"`)
	r.Contains(html, "Acme Corp — sent by SolidPing")
}
