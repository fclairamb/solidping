package whatsapp_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/integrations/whatsapp"
)

const sampleWebhook = `{
  "object": "whatsapp_business_account",
  "entry": [{
    "id": "WABA_ID",
    "changes": [{
      "field": "messages",
      "value": {
        "messaging_product": "whatsapp",
        "metadata": {"display_phone_number": "15550001111", "phone_number_id": "PNID"},
        "statuses": [
          {"id": "wamid.SENT", "status": "sent", "timestamp": "1700000000", "recipient_id": "33612345678"},
          {"id": "wamid.FAILED", "status": "failed", "timestamp": "1700000001", "recipient_id": "33600000000",
           "errors": [{"code": 131026, "title": "Message undeliverable",
                       "error_data": {"details": "Receiver is incapable of receiving this message"}}]}
        ],
        "messages": [
          {"from": "33612345678", "id": "wamid.IN", "timestamp": "1700000002", "type": "text",
           "text": {"body": "on it"}}
        ]
      }
    }]
  }]
}`

func TestParseWebhook(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	payload, err := whatsapp.ParseWebhook([]byte(sampleWebhook))
	r.NoError(err)
	r.Equal("whatsapp_business_account", payload.Object)

	statuses := payload.Statuses()
	r.Len(statuses, 2)
	r.Equal("wamid.SENT", statuses[0].ID)
	r.Equal(whatsapp.StatusSent, statuses[0].Status)
	r.Equal(whatsapp.StatusFailed, statuses[1].Status)
	r.Len(statuses[1].Errors, 1)
	r.Equal(131026, statuses[1].Errors[0].Code)

	messages := payload.InboundMessages()
	r.Len(messages, 1)
	r.Equal("on it", messages[0].Text.Body)
	r.Equal("33612345678", messages[0].From)
}

func TestParseWebhook_Invalid(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, err := whatsapp.ParseWebhook([]byte("not json"))
	r.Error(err)

	var nilPayload *whatsapp.WebhookPayload
	r.Nil(nilPayload.Statuses())
	r.Nil(nilPayload.InboundMessages())
}

func TestMessageStatus_Describe(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	payload, err := whatsapp.ParseWebhook([]byte(sampleWebhook))
	r.NoError(err)

	statuses := payload.Statuses()
	r.Equal("whatsapp status: sent", statuses[0].Describe())

	failed := statuses[1].Describe()
	r.True(strings.HasPrefix(failed, "whatsapp status: failed"))
	r.Contains(failed, "131026")
	r.Contains(failed, "Message undeliverable")
	r.Contains(failed, "Receiver is incapable of receiving this message")
}

func TestValidateSignature(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := []byte(`{"object":"whatsapp_business_account"}`)
	secret := "app-secret"

	good := whatsapp.Sign(secret, body)
	r.True(strings.HasPrefix(good, "sha256="))
	r.True(whatsapp.ValidateSignature(secret, body, good))

	// Wrong secret.
	r.False(whatsapp.ValidateSignature("other-secret", body, good))
	// Tampered body.
	r.False(whatsapp.ValidateSignature(secret, []byte(`{"object":"evil"}`), good))
	// Missing header.
	r.False(whatsapp.ValidateSignature(secret, body, ""))
	// Missing prefix.
	r.False(whatsapp.ValidateSignature(secret, body, strings.TrimPrefix(good, "sha256=")))
	// Not hex.
	r.False(whatsapp.ValidateSignature(secret, body, "sha256=zzzz"))
	// No configured secret can never pass.
	r.False(whatsapp.ValidateSignature("", body, good))
}
