package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/smsfactor"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
)

// smsFactorSentMessage is the part of an SMS Factor send body the tests read.
type smsFactorSentMessage struct {
	Auth   string
	Text   string
	Sender string
	To     []string
}

// smsFactorFake captures every send posted by the sender.
type smsFactorFake struct {
	mu       sync.Mutex
	messages []smsFactorSentMessage
	response string
}

func newSMSFactorFake(t *testing.T, response string) (*smsFactorFake, string) {
	t.Helper()

	f := &smsFactorFake{response: response}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)

		var body struct {
			SMS struct {
				Message struct {
					Text   string `json:"text"`
					Sender string `json:"sender"`
				} `json:"message"`
				Recipients struct {
					GSM []struct {
						Value string `json:"value"`
					} `json:"gsm"`
				} `json:"recipients"`
			} `json:"sms"`
		}
		_ = json.Unmarshal(raw, &body)

		msg := smsFactorSentMessage{
			Auth:   r.Header.Get("Authorization"),
			Text:   body.SMS.Message.Text,
			Sender: body.SMS.Message.Sender,
		}
		for _, g := range body.SMS.Recipients.GSM {
			msg.To = append(msg.To, g.Value)
		}

		f.mu.Lock()
		f.messages = append(f.messages, msg)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.response))
	}))
	t.Cleanup(srv.Close)

	return f, srv.URL
}

func (f *smsFactorFake) sent() []smsFactorSentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]smsFactorSentMessage(nil), f.messages...)
}

const smsFactorOK = `{"status":1,"message":"OK","ticket":"14672468","sent":1}`

func smsFactorPayload(event string, settings models.JSONMap) *Payload {
	payload := twilioPayload(event, settings)
	payload.Integration.Type = models.ConnectionTypeSMSFactor

	return payload
}

func fullSMSFactorSettings() models.JSONMap {
	return models.JSONMap{
		"token":      "sf-token",
		"sender":     "ACME",
		"to_numbers": []any{"+33612345678", "+33612345679"},
	}
}

func TestSMSFactorSender_SendsToAllRecipients(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, baseURL := newSMSFactorFake(t, smsFactorOK)
	sender := &SMSFactorSender{BaseURL: baseURL}

	payload := smsFactorPayload(eventTypeIncidentCreated, fullSMSFactorSettings())
	r.NoError(sender.Send(context.Background(), twilioJobCtx(), payload))

	sent := fake.sent()
	r.Len(sent, 2)
	r.Equal([]string{"33612345678"}, sent[0].To)
	r.Equal([]string{"33612345679"}, sent[1].To)
	r.Equal("Bearer sf-token", sent[0].Auth)
	r.Equal("ACME", sent[0].Sender)
	r.Contains(sent[0].Text, "is DOWN")
	r.Contains(sent[0].Text, "Ack:")
	r.Equal("14672468", payload.MessageID)
}

// TestSMSFactorSender_NoOptOutFooter pins that the Twilio (US A2P) opt-out
// footer stays a Twilio thing: SMS Factor bodies carry none.
func TestSMSFactorSender_NoOptOutFooter(t *testing.T) {
	t.Parallel()

	for _, eventType := range []string{
		eventTypeIncidentCreated,
		eventTypeIncidentReopened,
		eventTypeIncidentEscalated,
		eventTypeIncidentResolved,
	} {
		t.Run(eventType, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			fake, baseURL := newSMSFactorFake(t, smsFactorOK)
			sender := &SMSFactorSender{BaseURL: baseURL}

			settings := fullSMSFactorSettings()
			settings["to_numbers"] = []any{"+33612345678"}

			r.NoError(sender.Send(context.Background(), twilioJobCtx(), smsFactorPayload(eventType, settings)))

			sent := fake.sent()
			r.Len(sent, 1)
			r.NotEmpty(sent[0].Text)
			r.NotContains(sent[0].Text, twilio.OptOutFooter)
			r.NotContains(sent[0].Text, "STOP")
		})
	}
}

func TestSMSFactorSender_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mutate   func(models.JSONMap)
		response string
		wantErr  error
	}{
		{name: "missing token", mutate: func(m models.JSONMap) { delete(m, "token") }, wantErr: ErrSMSFactorNotConfigured},
		{
			name: "no recipients", mutate: func(m models.JSONMap) { delete(m, "to_numbers") },
			wantErr: ErrSMSFactorNoRecipients,
		},
		{
			name: "api rejection", response: `{"status":-3,"message":"Not enough credits"}`,
			wantErr: smsfactor.ErrRejected,
		},
		{
			name: "number filtered", response: `{"status":1,"message":"OK","ticket":"1","sent":0,"invalid":1}`,
			wantErr: smsfactor.ErrNoRecipientReached,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			response := tt.response
			if response == "" {
				response = smsFactorOK
			}

			_, baseURL := newSMSFactorFake(t, response)
			sender := &SMSFactorSender{BaseURL: baseURL}

			settings := fullSMSFactorSettings()
			if tt.mutate != nil {
				tt.mutate(settings)
			}

			err := sender.Send(context.Background(), twilioJobCtx(), smsFactorPayload(eventTypeIncidentCreated, settings))
			r.ErrorIs(err, tt.wantErr)
		})
	}
}

func TestSMSFactorSender_RegistryAndCapabilities(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender, ok := GetSender(models.ConnectionTypeSMSFactor)
	r.True(ok)
	r.IsType(&SMSFactorSender{}, sender)

	r.False(AcceptsEventType(models.ConnectionTypeSMSFactor, "incident.comment"))
	r.False(AcceptsEventType(models.ConnectionTypeSMSFactor, eventTypeIncidentAcknowledged))
	r.False(AcceptsEventType(models.ConnectionTypeSMSFactor, eventTypeIncidentReminder))
	r.True(AcceptsEventType(models.ConnectionTypeSMSFactor, eventTypeIncidentCreated))

	caps := models.CapabilitiesFor(models.ConnectionTypeSMSFactor)
	r.True(caps.CanNotify)
	r.True(caps.CanSendSMS)
	r.False(caps.CanPlaceCall)
}
