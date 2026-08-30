package whatsapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/integrations/whatsapp"
)

// fakeGraph spins up an httptest server standing in for graph.facebook.com and
// captures the last request it saw. Every test gets its own server — nothing
// here touches a package-level global, so the tests are safe under t.Parallel.
type fakeGraph struct {
	server *httptest.Server
	// captured request facts.
	path  string
	auth  string
	ctype string
	body  map[string]any
}

func newFakeGraph(t *testing.T, status int, response string) *fakeGraph {
	t.Helper()

	fake := &fakeGraph{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.path = r.URL.Path
		fake.auth = r.Header.Get("Authorization")
		fake.ctype = r.Header.Get("Content-Type")
		_ = json.Unmarshal(body, &fake.body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(fake.server.Close)

	return fake
}

func newTestClient(t *testing.T, fake *fakeGraph) *whatsapp.Client {
	t.Helper()

	client, err := whatsapp.NewClient(whatsapp.Options{
		BaseURL:       fake.server.URL,
		APIVersion:    "v23.0",
		PhoneNumberID: "1234567890",
		AccessToken:   "test-token",
	})
	require.NoError(t, err)

	return client
}

func TestSendTemplate_PayloadShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeGraph(t, http.StatusOK,
		`{"messaging_product":"whatsapp","messages":[{"id":"wamid.ABC"}]}`)
	client := newTestClient(t, fake)

	id, err := client.SendTemplate(context.Background(), &whatsapp.TemplateMessage{
		To:         "+33612345678",
		Template:   "solidping_alert",
		Language:   "fr",
		BodyParams: []string{"API", "DOWN", "connection refused", "acme"},
	})
	r.NoError(err)
	r.Equal("wamid.ABC", id)

	// Endpoint: /{api_version}/{phone_number_id}/messages
	r.Equal("/v23.0/1234567890/messages", fake.path)
	r.Equal("Bearer test-token", fake.auth)
	r.Equal("application/json", fake.ctype)

	r.Equal("whatsapp", fake.body["messaging_product"])
	r.Equal("individual", fake.body["recipient_type"])
	// The leading '+' is stripped on the wire.
	r.Equal("33612345678", fake.body["to"])
	r.Equal("template", fake.body["type"])

	tmpl, ok := fake.body["template"].(map[string]any)
	r.True(ok)
	r.Equal("solidping_alert", tmpl["name"])

	lang, ok := tmpl["language"].(map[string]any)
	r.True(ok)
	r.Equal("fr", lang["code"])

	components, ok := tmpl["components"].([]any)
	r.True(ok)
	// An empty ButtonParam emits NO button component: an installation whose
	// approved template has no button must keep working.
	r.Len(components, 1)

	body, ok := components[0].(map[string]any)
	r.True(ok)
	r.Equal("body", body["type"])

	params, ok := body["parameters"].([]any)
	r.True(ok)
	r.Len(params, 4)

	first, ok := params[0].(map[string]any)
	r.True(ok)
	r.Equal("text", first["type"])
	r.Equal("API", first["text"])

	last, ok := params[3].(map[string]any)
	r.True(ok)
	r.Equal("acme", last["text"])
}

func TestSendTemplate_AuthenticationTemplateAddsCopyCodeButton(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeGraph(t, http.StatusOK, `{"messages":[{"id":"wamid.OTP"}]}`)
	client := newTestClient(t, fake)

	_, err := client.SendTemplate(context.Background(), &whatsapp.TemplateMessage{
		To:          "+33612345678",
		Template:    "solidping_verify",
		Language:    "en",
		BodyParams:  []string{"123456"},
		ButtonParam: "123456",
	})
	r.NoError(err)

	tmpl, ok := fake.body["template"].(map[string]any)
	r.True(ok)

	components, ok := tmpl["components"].([]any)
	r.True(ok)
	r.Len(components, 2)

	button, ok := components[1].(map[string]any)
	r.True(ok)
	r.Equal("button", button["type"])
	r.Equal("url", button["sub_type"])
	r.Equal("0", button["index"])

	params, ok := button["parameters"].([]any)
	r.True(ok)
	r.Len(params, 1)

	param, ok := params[0].(map[string]any)
	r.True(ok)
	r.Equal("123456", param["text"])
}

// TestSendTemplate_AlertTemplateCarriesURLButtonSuffix proves the same
// ButtonParam field serves the alert template's "Visit website" button: the
// path suffix is carried verbatim, alongside the four body variables, in the
// component shape a dynamic URL button needs.
func TestSendTemplate_AlertTemplateCarriesURLButtonSuffix(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeGraph(t, http.StatusOK, `{"messages":[{"id":"wamid.LINK"}]}`)
	client := newTestClient(t, fake)

	_, err := client.SendTemplate(context.Background(), &whatsapp.TemplateMessage{
		To:          "+33612345678",
		Template:    "solidping_alert",
		Language:    "en",
		BodyParams:  []string{"API", "DOWN", "connection refused", "acme"},
		ButtonParam: "orgs/acme/checks/check-1",
	})
	r.NoError(err)

	tmpl, ok := fake.body["template"].(map[string]any)
	r.True(ok)

	components, ok := tmpl["components"].([]any)
	r.True(ok)
	r.Len(components, 2, "body + button")

	bodyComp, ok := components[0].(map[string]any)
	r.True(ok)
	r.Equal("body", bodyComp["type"])

	button, ok := components[1].(map[string]any)
	r.True(ok)
	r.Equal("button", button["type"])
	r.Equal("url", button["sub_type"])
	r.Equal("0", button["index"])

	params, ok := button["parameters"].([]any)
	r.True(ok)
	r.Len(params, 1)

	param, ok := params[0].(map[string]any)
	r.True(ok)
	r.Equal("text", param["type"])
	r.Equal("orgs/acme/checks/check-1", param["text"], "the suffix is passed through untouched")
}

func TestSendTemplate_DefaultsLanguageAndAPIVersion(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeGraph(t, http.StatusOK, `{"messages":[{"id":"wamid.D"}]}`)

	client, err := whatsapp.NewClient(whatsapp.Options{
		BaseURL:       fake.server.URL,
		PhoneNumberID: "999",
		AccessToken:   "tok",
	})
	r.NoError(err)

	_, err = client.SendTemplate(context.Background(), &whatsapp.TemplateMessage{
		To: "+15551234567", Template: "solidping_alert",
	})
	r.NoError(err)

	r.Equal("/"+config.DefaultWhatsAppAPIVersion+"/999/messages", fake.path)

	tmpl, ok := fake.body["template"].(map[string]any)
	r.True(ok)

	lang, ok := tmpl["language"].(map[string]any)
	r.True(ok)
	r.Equal(config.DefaultWhatsAppTemplateLanguage, lang["code"])
}

// TestSendTemplate_TypedErrors walks every failure class the spec requires us
// to handle, plus the HTTP-status fallbacks for unknown codes.
func TestSendTemplate_TypedErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		status   int
		response string
		want     error
		reason   string
	}{
		{
			name:   "template does not exist",
			status: http.StatusBadRequest,
			response: `{"error":{"message":"Template name does not exist","code":132001,` +
				`"error_subcode":2494010,"type":"OAuthException"}}`,
			want:   whatsapp.ErrTemplateUnavailable,
			reason: "whatsapp_template_unavailable",
		},
		{
			name:     "template paused",
			status:   http.StatusBadRequest,
			response: `{"error":{"message":"Template is paused","code":132015}}`,
			want:     whatsapp.ErrTemplateUnavailable,
			reason:   "whatsapp_template_unavailable",
		},
		{
			name:     "template disabled",
			status:   http.StatusBadRequest,
			response: `{"error":{"message":"Template is disabled","code":132016}}`,
			want:     whatsapp.ErrTemplateUnavailable,
			reason:   "whatsapp_template_unavailable",
		},
		{
			name:   "recipient not on whatsapp",
			status: http.StatusBadRequest,
			response: `{"error":{"message":"(#131026) Message undeliverable","type":"OAuthException",` +
				`"code":131026,"error_data":{"messaging_product":"whatsapp","details":` +
				`"Receiver is incapable of receiving this message"},"fbtrace_id":"Ax1"}}`,
			want:   whatsapp.ErrRecipientNotOnWhatsApp,
			reason: "whatsapp_recipient_not_on_whatsapp",
		},
		// 131049 is a per-user PACING drop, not a bad number. Filing it under
		// "recipient is not on WhatsApp" would tell an operator to delete a
		// perfectly good contact instead of letting the next repeat retry.
		{
			name:   "per-user engagement pacing is rate limiting, not a bad recipient",
			status: http.StatusBadRequest,
			response: `{"error":{"message":"(#131049) This message was not delivered to maintain ` +
				`healthy ecosystem engagement","type":"OAuthException","code":131049,` +
				`"error_data":{"messaging_product":"whatsapp","details":` +
				`"This message was not delivered to maintain healthy ecosystem engagement."},` +
				`"fbtrace_id":"Ax2"}}`,
			want:   whatsapp.ErrRateLimited,
			reason: "whatsapp_rate_limited",
		},
		// 131051 is OUR payload being wrong. It must surface as our bug, never
		// as "this number is invalid".
		{
			name:   "unsupported message type is our bug, not a bad recipient",
			status: http.StatusBadRequest,
			response: `{"error":{"message":"(#131051) Unsupported message type","type":"OAuthException",` +
				`"code":131051,"error_data":{"messaging_product":"whatsapp","details":` +
				`"Message type is not currently supported"},"fbtrace_id":"Ax3"}}`,
			want:   whatsapp.ErrUnsupportedMessage,
			reason: "whatsapp_unsupported_message",
		},
		{
			name:   "re-engagement window closed",
			status: http.StatusBadRequest,
			response: `{"error":{"message":"(#131047) Re-engagement message","type":"OAuthException",` +
				`"code":131047,"error_data":{"messaging_product":"whatsapp","details":` +
				`"Message failed to send because more than 24 hours have passed since the ` +
				`customer last replied to this number"},"fbtrace_id":"Ax4"}}`,
			want:   whatsapp.ErrSessionWindowClosed,
			reason: "whatsapp_session_window_closed",
		},
		{
			name:     "messaging tier cap",
			status:   http.StatusBadRequest,
			response: `{"error":{"message":"Rate limit hit","code":130429}}`,
			want:     whatsapp.ErrRateLimited,
			reason:   "whatsapp_rate_limited",
		},
		{
			name:     "app request limit",
			status:   http.StatusBadRequest,
			response: `{"error":{"message":"Application request limit reached","code":4}}`,
			want:     whatsapp.ErrRateLimited,
			reason:   "whatsapp_rate_limited",
		},
		{
			name:   "token expired",
			status: http.StatusUnauthorized,
			response: `{"error":{"message":"Error validating access token","code":190,` +
				`"error_subcode":463,"type":"OAuthException"}}`,
			want:   whatsapp.ErrTokenExpired,
			reason: "whatsapp_token_expired",
		},
		{
			name:     "unknown code falls back to 429 class",
			status:   http.StatusTooManyRequests,
			response: `{"error":{"message":"slow down","code":999999}}`,
			want:     whatsapp.ErrRateLimited,
			reason:   "whatsapp_rate_limited",
		},
		{
			name:     "unknown code falls back to 403 class",
			status:   http.StatusForbidden,
			response: `{"error":{"message":"nope","code":999998}}`,
			want:     whatsapp.ErrTokenExpired,
			reason:   "whatsapp_token_expired",
		},
		{
			name:     "unclassified",
			status:   http.StatusInternalServerError,
			response: `{"error":{"message":"boom","code":1}}`,
			want:     whatsapp.ErrRequestFailed,
			reason:   "whatsapp_send_failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			fake := newFakeGraph(t, tc.status, tc.response)
			client := newTestClient(t, fake)

			_, err := client.SendTemplate(context.Background(), &whatsapp.TemplateMessage{
				To: "+15551234567", Template: "solidping_alert",
			})
			r.Error(err)
			r.ErrorIs(err, tc.want)
			r.Equal(tc.reason, whatsapp.FailureReason(err))

			var apiErr *whatsapp.APIError
			r.ErrorAs(err, &apiErr)
			r.Equal(tc.status, apiErr.StatusCode)
			r.NotEmpty(apiErr.Message)
		})
	}
}

func TestSendTemplate_NoMessageID(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeGraph(t, http.StatusOK, `{"messaging_product":"whatsapp","messages":[]}`)
	client := newTestClient(t, fake)

	_, err := client.SendTemplate(context.Background(), &whatsapp.TemplateMessage{
		To: "+15551234567", Template: "solidping_alert",
	})
	r.ErrorIs(err, whatsapp.ErrNoMessageID)
}

func TestSendTemplate_ValidatesInput(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeGraph(t, http.StatusOK, `{"messages":[{"id":"wamid.X"}]}`)
	client := newTestClient(t, fake)

	_, err := client.SendTemplate(context.Background(), &whatsapp.TemplateMessage{
		To: "not-a-number", Template: "solidping_alert",
	})
	r.ErrorIs(err, whatsapp.ErrInvalidRecipient)
	r.Equal("whatsapp_invalid_recipient", whatsapp.FailureReason(err))

	_, err = client.SendTemplate(context.Background(), &whatsapp.TemplateMessage{
		To: "+15551234567",
	})
	r.ErrorIs(err, whatsapp.ErrMissingTemplate)

	// A bare (no '+') E.164 number is accepted — Meta's own format.
	_, err = client.SendTemplate(context.Background(), &whatsapp.TemplateMessage{
		To: "15551234567", Template: "solidping_alert",
	})
	r.NoError(err)
	r.Equal("15551234567", fake.body["to"])
}

func TestMarkRead_PayloadShapeAndSuccess(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeGraph(t, http.StatusOK, `{"success":true}`)
	client := newTestClient(t, fake)

	err := client.MarkRead(context.Background(), "wamid.INBOUND123")
	r.NoError(err)

	// Same endpoint as sends: /{api_version}/{phone_number_id}/messages
	r.Equal("/v23.0/1234567890/messages", fake.path)
	r.Equal("Bearer test-token", fake.auth)
	r.Equal("application/json", fake.ctype)

	r.Equal("whatsapp", fake.body["messaging_product"])
	r.Equal("read", fake.body["status"])
	r.Equal("wamid.INBOUND123", fake.body["message_id"])
	// The mark-read payload has no "to" / "type" / "recipient_type" — it is
	// not a message send.
	r.NotContains(fake.body, "to")
	r.NotContains(fake.body, "type")
}

func TestMarkRead_SuccessFalseIsAFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Graph returned 2xx but the body says the mark-read did not take —
	// this must not be silently treated as success.
	fake := newFakeGraph(t, http.StatusOK, `{"success":false}`)
	client := newTestClient(t, fake)

	err := client.MarkRead(context.Background(), "wamid.INBOUND123")
	r.ErrorIs(err, whatsapp.ErrRequestFailed)
}

func TestMarkRead_RequiresAMessageID(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeGraph(t, http.StatusOK, `{"success":true}`)
	client := newTestClient(t, fake)

	err := client.MarkRead(context.Background(), "   ")
	r.ErrorIs(err, whatsapp.ErrMissingMessageID)
}

func TestMarkRead_ClassifiesGraphErrorsThroughTheSameSentinels(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeGraph(t, http.StatusUnauthorized,
		`{"error":{"message":"Error validating access token","code":190,`+
			`"error_subcode":463,"type":"OAuthException"}}`)
	client := newTestClient(t, fake)

	err := client.MarkRead(context.Background(), "wamid.INBOUND123")
	r.Error(err)
	r.ErrorIs(err, whatsapp.ErrTokenExpired)
	r.Equal("whatsapp_token_expired", whatsapp.FailureReason(err))

	var apiErr *whatsapp.APIError
	r.ErrorAs(err, &apiErr)
	r.Equal(http.StatusUnauthorized, apiErr.StatusCode)
}

func TestNewClient_NotConfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, err := whatsapp.NewClient(whatsapp.Options{PhoneNumberID: "1"})
	r.ErrorIs(err, whatsapp.ErrNotConfigured)

	_, err = whatsapp.NewClient(whatsapp.Options{AccessToken: "t"})
	r.ErrorIs(err, whatsapp.ErrNotConfigured)

	// Kill switch off → not configured even with full credentials.
	_, err = whatsapp.NewClientFromConfig(&config.WhatsAppConfig{
		Enabled: false, AccessToken: "t", PhoneNumberID: "1",
	})
	r.ErrorIs(err, whatsapp.ErrNotConfigured)

	client, err := whatsapp.NewClientFromConfig(&config.WhatsAppConfig{
		Enabled: true, AccessToken: "t", PhoneNumberID: "1",
	})
	r.NoError(err)
	r.NotNil(client)

	r.Empty(whatsapp.FailureReason(nil))
	r.Equal("whatsapp_not_configured", whatsapp.FailureReason(whatsapp.ErrNotConfigured))
}

func TestValidE164(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.True(whatsapp.ValidE164("+15551234567"))
	r.False(whatsapp.ValidE164("15551234567"))
	r.False(whatsapp.ValidE164("+0555123456"))
	r.False(whatsapp.ValidE164(""))
}

// TestErrorClasses_AreMutuallyExclusive is the guard against the failure mode
// this taxonomy exists to prevent: a code landing in a class that produces a
// *misleading* operator instruction. Asserting only "an error came back" would
// have let 131049 (pacing) and 131051 (our payload bug) masquerade as
// "recipient is not on WhatsApp" indefinitely.
func TestErrorClasses_AreMutuallyExclusive(t *testing.T) {
	t.Parallel()

	cases := map[int]struct {
		want    error
		notWant []error
	}{
		131026: {
			want:    whatsapp.ErrRecipientNotOnWhatsApp,
			notWant: []error{whatsapp.ErrRateLimited, whatsapp.ErrUnsupportedMessage},
		},
		131049: {
			want:    whatsapp.ErrRateLimited,
			notWant: []error{whatsapp.ErrRecipientNotOnWhatsApp, whatsapp.ErrUnsupportedMessage},
		},
		131051: {
			want:    whatsapp.ErrUnsupportedMessage,
			notWant: []error{whatsapp.ErrRecipientNotOnWhatsApp, whatsapp.ErrRateLimited},
		},
		131047: {
			want:    whatsapp.ErrSessionWindowClosed,
			notWant: []error{whatsapp.ErrRecipientNotOnWhatsApp, whatsapp.ErrRateLimited},
		},
		132015: {
			want:    whatsapp.ErrTemplateUnavailable,
			notWant: []error{whatsapp.ErrRecipientNotOnWhatsApp, whatsapp.ErrUnsupportedMessage},
		},
	}

	for code, tc := range cases {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			fake := newFakeGraph(t, http.StatusBadRequest,
				fmt.Sprintf(`{"error":{"message":"(#%d) something","type":"OAuthException","code":%d,`+
					`"error_data":{"messaging_product":"whatsapp","details":"detail"},`+
					`"fbtrace_id":"Ax"}}`, code, code))
			client := newTestClient(t, fake)

			_, err := client.SendTemplate(context.Background(), &whatsapp.TemplateMessage{
				To: "+15551234567", Template: "solidping_alert",
			})
			r.ErrorIs(err, tc.want)

			for _, other := range tc.notWant {
				r.NotErrorIs(err, other, "code %d must not be classified as %v", code, other)
			}

			var apiErr *whatsapp.APIError
			r.ErrorAs(err, &apiErr)
			r.Equal(code, apiErr.Code)
			r.Equal("OAuthException", apiErr.Type)
		})
	}
}
