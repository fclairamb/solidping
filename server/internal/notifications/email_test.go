package notifications

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// errFakeSendFailed is the static error returned by fakeEmailSender when it is
// configured to fail, satisfying the err113 linter (no dynamic errors).
var errFakeSendFailed = errors.New("fake: send failed")

// fakeEmailSender is a test double for email.Sender that records the messages it
// was asked to send, so a test can assert the resolved recipients without
// sending real mail. It synthesizes a per-send Message-ID and SMTP server
// response derived from the recipients so callers can assert the audit-row
// artifacts the real sender now surfaces.
type fakeEmailSender struct {
	sent []*email.Message
	// failAfter, when > 0, makes the Nth send (1-indexed) return an error after
	// the preceding sends succeed, exercising the partial-batch path.
	failAfter int
}

func (f *fakeEmailSender) Send(_ context.Context, msg *email.Message) (*email.SendResult, error) {
	f.sent = append(f.sent, msg)

	if f.failAfter > 0 && len(f.sent) == f.failAfter {
		return nil, errFakeSendFailed
	}

	key := strings.Join(msg.Recipients.To, ",")

	return &email.SendResult{
		Sent:           true,
		Message:        "fake: email sent",
		MessageID:      "mid-" + key,
		ServerResponse: "250 2.0.0 Ok: queued as Q-" + key,
	}, nil
}

// newTestFormatter builds a real TemplateFormatter — these tests exercise the
// actual rendering pipeline (D1: one pipeline for every email) rather than a
// stub, so a formatter-wiring regression fails here, not just in
// server/internal/email's own package tests.
func newTestFormatter(t *testing.T) email.Formatter {
	t.Helper()

	f, err := email.NewFormatter()
	require.NoError(t, err)

	return f
}

// TestEmailSender_Send_RecipientResolution pins recipient resolution from the
// canonical "to" settings key. The dashboard writes recipients under "to", so
// the backend must read from "to" — never "recipients" (the historical bug).
//
// A non-modeled event type is used so Send takes the single broadcast path (no
// per-recipient personalization needed). The generic event type also avoids
// the incident-template rendering path, so the assertion stays focused on
// recipient resolution and depends only on the Check (for the subject) and
// Integration.Settings.
func TestEmailSender_Send_RecipientResolution(t *testing.T) {
	t.Parallel()

	checkName := "API health"

	tests := []struct {
		name string
		// settings is the integration's stored Settings JSONB.
		settings models.JSONMap
		// wantErr, when non-nil, is the sentinel error Send must return; in that
		// case no email is sent.
		wantErr error
		// wantTo is the resolved recipient list asserted on the sent message
		// (only checked when wantErr is nil).
		wantTo []string
	}{
		{
			name:     "to as []any resolves and attempts send",
			settings: models.JSONMap{"to": []any{"a@x.test"}},
			wantTo:   []string{"a@x.test"},
		},
		{
			name:     "to as []string resolves both recipients",
			settings: models.JSONMap{"to": []string{"a@x.test", "b@y.test"}},
			wantTo:   []string{"a@x.test", "b@y.test"},
		},
		{
			name:     "to absent returns ErrNoRecipientsConfigured",
			settings: models.JSONMap{},
			wantErr:  ErrNoRecipientsConfigured,
		},
		{
			name:     "to empty returns ErrNoRecipientsConfigured",
			settings: models.JSONMap{"to": []any{}},
			wantErr:  ErrNoRecipientsConfigured,
		},
		{
			name:     "to present but no usable string returns ErrNoValidRecipients",
			settings: models.JSONMap{"to": []any{42, "", nil}},
			wantErr:  ErrNoValidRecipients,
		},
		{
			name: "recipients key present but to absent still returns ErrNoRecipientsConfigured",
			// Pins "to" as the canonical key: the legacy "recipients" key must
			// not be honored, so a record carrying only "recipients" errors.
			settings: models.JSONMap{"recipients": []any{"legacy@x.test"}},
			wantErr:  ErrNoRecipientsConfigured,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			sender := &fakeEmailSender{}

			s := &EmailSender{}
			payload := &Payload{
				// A generic (non-modeled) event type takes the broadcast path
				// and hits the fallback email builder, which reads no
				// incident fields.
				EventType: "incident.updated",
				Check:     &models.Check{Name: &checkName, Type: "http"},
				Integration: &models.Integration{
					Settings: tt.settings,
				},
			}

			jctx := &jobdef.JobContext{
				Services: &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
				Logger:   slog.Default(),
			}

			err := s.Send(context.Background(), jctx, payload)

			if tt.wantErr != nil {
				r.ErrorIs(err, tt.wantErr)
				r.Empty(sender.sent, "no email should be sent when recipients do not resolve")

				return
			}

			r.NoError(err)
			r.Len(sender.sent, 1, "exactly one broadcast email should be sent")
			r.Equal(tt.wantTo, sender.sent[0].Recipients.To)
		})
	}
}

// TestEmailSender_Send_MissingFormatterErrors pins that Send fails fast with
// a clear sentinel when the email formatter service is not wired — every
// modeled incident event now renders through the formatter (D1), so a nil
// formatter must not silently skip rendering.
func TestEmailSender_Send_MissingFormatterErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	checkName := "API health"
	sender := &fakeEmailSender{}
	s := &EmailSender{}
	payload := &Payload{
		EventType: eventTypeIncidentCreated,
		Check:     &models.Check{Name: &checkName, Type: "http"},
		Incident:  &models.Incident{UID: "inc-1", StartedAt: time.Now()},
		Integration: &models.Integration{
			Settings: models.JSONMap{"to": []any{"a@x.test"}},
		},
	}

	jctx := &jobdef.JobContext{
		Services:  &services.Registry{EmailSender: sender}, // EmailFormatter deliberately nil
		AppConfig: &config.Config{},
		Logger:    slog.Default(),
	}

	err := s.Send(context.Background(), jctx, payload)
	r.ErrorIs(err, ErrEmailFormatterNotConfigured)
	r.Empty(sender.sent)
}

// TestEmailSender_Send_CapturesDeliveryArtifacts pins that a successful send
// surfaces the SMTP Message-ID and per-recipient server response on the payload
// so the notification audit row can prove the message was handed off.
func TestEmailSender_Send_CapturesDeliveryArtifacts(t *testing.T) {
	t.Parallel()

	checkName := "API health"

	t.Run("broadcast send records first message id and transcript", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		sender := &fakeEmailSender{}
		s := &EmailSender{}
		payload := &Payload{
			EventType: "incident.updated", // non-modeled → single broadcast send
			Check:     &models.Check{Name: &checkName, Type: "http"},
			Integration: &models.Integration{
				Settings: models.JSONMap{"to": []any{"a@x.test", "b@y.test"}},
			},
		}

		jctx := &jobdef.JobContext{
			Services: &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
			Logger:   slog.Default(),
		}

		r.NoError(s.Send(context.Background(), jctx, payload))

		r.Equal("mid-a@x.test,b@y.test", payload.MessageID)
		r.NotNil(payload.DeliveryDetails)
		r.Contains(payload.DeliveryDetails.ResponseBody, "recipients: a@x.test, b@y.test")
		r.Contains(payload.DeliveryDetails.ResponseBody, "message-id: mid-a@x.test,b@y.test")
		r.Contains(payload.DeliveryDetails.ResponseBody, "server: 250 2.0.0 Ok: queued as Q-a@x.test,b@y.test")
	})

	t.Run("per-recipient send records first id and every server response", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		sender := &fakeEmailSender{}
		s := &EmailSender{}
		payload := &Payload{
			EventType: eventTypeIncidentCreated, // modeled event → one send per recipient
			Check:     &models.Check{Name: &checkName, Type: "http"},
			Incident:  &models.Incident{UID: "inc-1", StartedAt: time.Now()},
			Integration: &models.Integration{
				Settings: models.JSONMap{"to": []any{"a@x.test", "b@y.test"}},
			},
		}

		jctx := &jobdef.JobContext{
			Services:  &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
			AppConfig: &config.Config{},
			Logger:    slog.Default(),
		}

		r.NoError(s.Send(context.Background(), jctx, payload))

		r.Len(sender.sent, 2, "one send per recipient")
		// First Message-ID is the correlation handle stored on the row.
		r.Equal("mid-a@x.test", payload.MessageID)
		r.NotNil(payload.DeliveryDetails)
		// The transcript carries every recipient's own server response.
		r.Contains(payload.DeliveryDetails.ResponseBody, "server: 250 2.0.0 Ok: queued as Q-a@x.test")
		r.Contains(payload.DeliveryDetails.ResponseBody, "server: 250 2.0.0 Ok: queued as Q-b@y.test")
	})

	t.Run("resolved event also sends per recipient (needed for per-recipient unsubscribe tokens)", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		sender := &fakeEmailSender{}
		s := &EmailSender{}
		resolvedAt := time.Now()
		payload := &Payload{
			EventType: eventTypeIncidentResolved,
			Check:     &models.Check{Name: &checkName, Type: "http"},
			Incident: &models.Incident{
				UID: "inc-1", StartedAt: resolvedAt.Add(-time.Hour), ResolvedAt: &resolvedAt,
			},
			Integration: &models.Integration{
				Settings: models.JSONMap{"to": []any{"a@x.test", "b@y.test"}},
			},
		}

		jctx := &jobdef.JobContext{
			Services:  &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
			AppConfig: &config.Config{},
			Logger:    slog.Default(),
		}

		r.NoError(s.Send(context.Background(), jctx, payload))
		r.Len(sender.sent, 2, "resolved must send one email per recipient, not one broadcast")

		for _, msg := range sender.sent {
			r.NotContains(msg.HTML, "Acknowledge incident", "resolved has nothing to ack")
		}
	})

	t.Run("partial batch failure still records delivered recipients", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		sender := &fakeEmailSender{failAfter: 2} // first recipient sends, second fails
		s := &EmailSender{}
		payload := &Payload{
			EventType: eventTypeIncidentCreated,
			Check:     &models.Check{Name: &checkName, Type: "http"},
			Incident:  &models.Incident{UID: "inc-1", StartedAt: time.Now()},
			Integration: &models.Integration{
				Settings: models.JSONMap{"to": []any{"a@x.test", "b@y.test"}},
			},
		}

		jctx := &jobdef.JobContext{
			Services:  &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
			AppConfig: &config.Config{},
			Logger:    slog.Default(),
		}

		// The send short-circuits on the failing recipient and returns the error.
		r.Error(s.Send(context.Background(), jctx, payload))

		// The recipient that did go out is still surfaced for the audit row.
		r.Equal("mid-a@x.test", payload.MessageID)
		r.NotNil(payload.DeliveryDetails)
		r.Contains(payload.DeliveryDetails.ResponseBody, "recipients: a@x.test")
		r.NotContains(payload.DeliveryDetails.ResponseBody, "Q-b@y.test")
	})
}

// TestEmailSender_Send_RendersIncidentTemplates is a focused integration test
// (real formatter, fake sender) confirming each modeled incident event
// renders through its dedicated template with the expected content — the
// "View incident" / check-link URLs (D3) and the ack button (ackable events
// only).
func TestEmailSender_Send_RendersIncidentTemplates(t *testing.T) {
	t.Parallel()

	checkName := "Production API"
	checkSlug := "prod-api"

	cases := []struct {
		name          string
		eventType     string
		wantSubjectIn string
		wantAckButton bool
	}{
		{"created", eventTypeIncidentCreated, "[DOWN]", true},
		{"resolved", eventTypeIncidentResolved, "[RECOVERED]", false},
		{"escalated", eventTypeIncidentEscalated, "[ESCALATED]", true},
		{"reopened", eventTypeIncidentReopened, "[REOPENED]", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			sender := &fakeEmailSender{}
			s := &EmailSender{}
			startedAt := time.Now().Add(-time.Hour)
			resolvedAt := time.Now()
			payload := &Payload{
				EventType: tc.eventType,
				Check:     &models.Check{UID: "check-1", Name: &checkName, Slug: &checkSlug, Type: "http"},
				Incident: &models.Incident{
					UID: "inc-42", StartedAt: startedAt, ResolvedAt: &resolvedAt,
					FailureCount: 3, RelapseCount: 1,
				},
				Integration: &models.Integration{
					Settings: models.JSONMap{"to": []any{"a@x.test"}},
				},
				OrgSlug:    "acme",
				AppBaseURL: "https://solidping.example",
			}

			jctx := &jobdef.JobContext{
				Services: &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
				AppConfig: &config.Config{
					Auth:   config.AuthConfig{JWTSecret: "test-secret"},
					Server: config.ServerConfig{BaseURL: "https://solidping.example"},
				},
				Logger: slog.Default(),
			}

			r.NoError(s.Send(context.Background(), jctx, payload))
			r.Len(sender.sent, 1)

			msg := sender.sent[0]
			r.Contains(msg.Subject, tc.wantSubjectIn)
			r.Contains(msg.HTML, "https://solidping.example/dash0/orgs/acme/incidents/inc-42", "View incident URL (D3)")
			r.Contains(msg.HTML, "https://solidping.example/dash0/orgs/acme/checks/prod-api", "check name link (D3)")
			r.Contains(msg.HTML, "https://solidping.example/dash0", "footer dashboard link")
			r.Contains(msg.HTML, "https://solidping.example/docs", "footer docs link")
			r.NotContains(msg.HTML, "{{", "no unresolved template syntax")
			r.NotEmpty(msg.Text, "text alternative must be populated")
			r.Contains(msg.Text, "https://solidping.example/dash0/orgs/acme/incidents/inc-42")

			if tc.wantAckButton {
				r.Contains(msg.HTML, "Acknowledge incident")
			} else {
				r.NotContains(msg.HTML, "Acknowledge incident")
			}
		})
	}
}
