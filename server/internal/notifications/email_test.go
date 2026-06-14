package notifications

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// fakeEmailSender is a test double for email.Sender that records the messages it
// was asked to send, so a test can assert the resolved recipients without
// sending real mail.
type fakeEmailSender struct {
	sent []*email.Message
}

func (f *fakeEmailSender) Send(_ context.Context, msg *email.Message) (*email.SendResult, error) {
	f.sent = append(f.sent, msg)

	return &email.SendResult{Sent: true, Message: "fake: email sent"}, nil
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
