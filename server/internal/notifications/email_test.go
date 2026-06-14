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

// TestEmailSender_Send_RecipientResolution pins recipient resolution from the
// canonical "to" settings key. The dashboard writes recipients under "to", so
// the backend must read from "to" — never "recipients" (the historical bug).
//
// A non-ack event type is used so Send takes the single broadcast path (no
// per-recipient ack-link personalization needed). The generic event type also
// avoids the incident-specific email builders, so the assertion stays focused on
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
				// A generic (non-ack) event type takes the broadcast path and
				// hits the default email builder, which reads no incident fields.
				EventType: "incident.updated",
				Check:     &models.Check{Name: &checkName, Type: "http"},
				Integration: &models.Integration{
					Settings: tt.settings,
				},
			}

			jctx := &jobdef.JobContext{
				Services: &services.Registry{EmailSender: sender},
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
			EventType: "incident.updated", // non-ack → single broadcast send
			Check:     &models.Check{Name: &checkName, Type: "http"},
			Integration: &models.Integration{
				Settings: models.JSONMap{"to": []any{"a@x.test", "b@y.test"}},
			},
		}

		jctx := &jobdef.JobContext{
			Services: &services.Registry{EmailSender: sender},
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
			EventType: eventTypeIncidentCreated, // ack event → one send per recipient
			Check:     &models.Check{Name: &checkName, Type: "http"},
			Incident:  &models.Incident{UID: "inc-1", StartedAt: time.Now()},
			Integration: &models.Integration{
				Settings: models.JSONMap{"to": []any{"a@x.test", "b@y.test"}},
			},
		}

		jctx := &jobdef.JobContext{
			Services:  &services.Registry{EmailSender: sender},
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
			Services:  &services.Registry{EmailSender: sender},
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
