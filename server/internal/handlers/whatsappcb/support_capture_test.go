package whatsappcb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/whatsapp"
	"github.com/fclairamb/solidping/server/internal/support"
)

// withSupportInbox attaches a support inbox over the env's own database.
func (e *wsEnv) withSupportInbox(t *testing.T) *support.Service {
	t.Helper()

	svc := support.NewService(e.db, support.Options{BaseURL: "https://solidping.example"})
	e.handler.support = svc

	return svc
}

// postSigned fires a correctly-signed webhook.
func (e *wsEnv) postSigned(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost,
		"/api/v1/integrations/whatsapp/webhook", strings.NewReader(body),
	)
	req.Header.Set("X-Hub-Signature-256", whatsapp.Sign(testAppSecret, []byte(body)))

	rec := httptest.NewRecorder()
	require.NoError(t, e.handler.HandleEvent(rec, req))

	return rec
}

// inboundBody renders one inbound message webhook.
func inboundBody(id, from, msgType, text string) string {
	return `{"object":"whatsapp_business_account","entry":[{"id":"WABA","changes":[{"field":"messages",` +
		`"value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"PNID"},` +
		`"messages":[{"from":"` + from + `","id":"` + id + `","timestamp":"1755900000",` +
		`"type":"` + msgType + `","text":{"body":"` + text + `"}}]}}]}]}`
}

func threadsOf(t *testing.T, svc *support.Service) []*models.SupportThread {
	t.Helper()

	threads, err := svc.ListThreads(context.Background(), models.ListSupportThreadsFilter{})
	require.NoError(t, err)

	return threads
}

// TestInboundMessageIsCaptured is the regression test for the incident the spec
// opens with: a real person replied to a WhatsApp alert and the only surviving
// record was the message id and the type. The body and the sender were parsed
// and then thrown away.
func TestInboundMessageIsCaptured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	rec := env.postSigned(t, inboundBody("wamid.IN1", "33612345678", "text", "is the api down?"))
	r.Equal(http.StatusNoContent, rec.Code)

	threads := threadsOf(t, svc)
	r.Len(threads, 1)
	// Meta sends `from` without a leading '+'; the thread identity is E.164.
	r.Equal("+33612345678", threads[0].ChannelIdentity)
	r.Equal(models.SupportChannelWhatsApp, threads[0].Channel)
	r.NotNil(threads[0].LastInboundAt)

	messages, err := svc.ListMessages(context.Background(), threads[0].UID, 0)
	r.NoError(err)
	r.Len(messages, 1)
	r.Equal("is the api down?", messages[0].Body, "the body must survive")
	r.Equal(models.SupportRawTypeText, messages[0].RawType)

	// The 24-hour window opened by this message is derivable, which is the
	// other half of what was lost: today that window opens and closes without
	// anyone knowing it existed.
	window := threads[0].ReplyWindow(*threads[0].LastInboundAt)
	r.True(window.Expires)
	r.True(window.Open)
}

// TestInboundRetryDoesNotDoubleInsert covers Meta's guaranteed webhook retry.
func TestInboundRetryDoesNotDoubleInsert(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	body := inboundBody("wamid.RETRY", "33612345678", "text", "hello")
	r.Equal(http.StatusNoContent, env.postSigned(t, body).Code)
	r.Equal(http.StatusNoContent, env.postSigned(t, body).Code)

	threads := threadsOf(t, svc)
	r.Len(threads, 1)

	messages, err := svc.ListMessages(context.Background(), threads[0].UID, 0)
	r.NoError(err)
	r.Len(messages, 1, "a retried webhook must not double-insert")
}

// TestInboundNonTextRecordsAPlaceholder — "someone sent a photo" is
// information; an empty body is not.
func TestInboundNonTextRecordsAPlaceholder(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	r.Equal(http.StatusNoContent,
		env.postSigned(t, inboundBody("wamid.IMG", "33612345678", "image", "")).Code)

	threads := threadsOf(t, svc)
	r.Len(threads, 1)

	messages, err := svc.ListMessages(context.Background(), threads[0].UID, 0)
	r.NoError(err)
	r.Len(messages, 1)
	r.Equal(models.SupportRawTypeImage, messages[0].RawType)
	r.NotEmpty(messages[0].Body)
	r.Contains(messages[0].Body, "image")
}

// TestCaptureFailureStillReturns204 is the negative control that matters most:
// a dead database must not turn a webhook into a non-2xx. Meta retries on any
// non-2xx and eventually disables the subscription, so a capture bug would cost
// the whole channel rather than one message.
func TestCaptureFailureStillReturns204(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	// Positive control first: with a live database the capture works, so the
	// assertion below cannot be passing because capture never runs.
	r.Equal(http.StatusNoContent,
		env.postSigned(t, inboundBody("wamid.OK", "33612345678", "text", "hi")).Code)
	r.Len(threadsOf(t, svc), 1)

	r.NoError(env.db.Close())

	rec := env.postSigned(t, inboundBody("wamid.DEAD", "33699999999", "text", "anyone there?"))
	r.Equal(http.StatusNoContent, rec.Code,
		"a capture failure must never break the channel it came from")
}

// TestDeliveryStatusesStillWorkWithCaptureOn proves the capture branch did not
// disturb the pre-existing delivery-status path they share a webhook with.
func TestDeliveryStatusesStillWorkWithCaptureOn(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	r.Equal(http.StatusNoContent, env.postSigned(t, statusBody(testMessageID, "delivered")).Code)
	r.Contains(env.deliveryBody(t), "delivered")

	// A status callback is not a human message.
	r.Empty(threadsOf(t, svc))
}

// panickingDB is a db.Service whose first query panics. The embedded nil
// interface makes every other method panic too.
type panickingDB struct {
	db.Service
}

func (panickingDB) DB() *bun.DB {
	panic("capture blew up inside the support service")
}

// TestCapturePanicStillReturns204 is the webhook-level half of CaptureSafe's
// contract, and the one that names the real cost.
//
// TestCaptureFailureStillReturns204 above covers a DB that returns errors. This
// one covers a capture path that PANICS — a nil map, a slice index, a parser
// tripping on a malformed provider payload. None of those are a returned error,
// and an escaping panic unwinds the webhook handler: Meta sees a 5xx, retries,
// and eventually disables the subscription. One bug would cost the whole
// channel rather than one message.
func TestCapturePanicStillReturns204(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	// A support service over a database that panics on first contact.
	env.handler.support = support.NewService(panickingDB{}, support.Options{})

	rec := env.postSigned(t, inboundBody("wamid.PANIC", "33612345678", "text", "hello"))
	r.Equal(http.StatusNoContent, rec.Code,
		"a panic inside capture must never escape into webhook handling")

	// Delivery statuses on the same webhook are unaffected: the alerting path
	// keeps working even while capture is broken.
	r.Equal(http.StatusNoContent, env.postSigned(t, statusBody(testMessageID, "delivered")).Code)
	r.Contains(env.deliveryBody(t), "delivered")
}
