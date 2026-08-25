package twiliocb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/support"
)

// smsEnv is a callback env whose instance SMS credentials are Twilio, which is
// what the inbound-SMS route validates against — the URL is configured once on
// the Messaging Service and carries no `cid`.
func setupInboundSMSEnv(t *testing.T) (*cbEnv, *support.Service) {
	t.Helper()

	env := setupCallbackEnv(t)
	env.cfg.SMS = config.SMSConfig{
		Enabled:  true,
		Provider: config.SMSProviderTwilio,
		Sender:   "+15559990000",
		Twilio: config.SMSTwilioConfig{
			AccountSID: "AC00000000000000000000000000000001",
			AuthToken:  testAuthToken,
		},
	}

	inbox := support.NewService(env.db, support.Options{BaseURL: "https://solidping.example"})
	env.handler.support = inbox

	return env, inbox
}

// testInboundNumber is the sender every inbound-SMS test writes from — a single
// identity keeps the threads it opens comparable across cases.
const testInboundNumber = "+33612345678"

func postInboundSMS(t *testing.T, env *cbEnv, body, sid string) *httptest.ResponseRecorder {
	t.Helper()

	form := url.Values{"From": {testInboundNumber}, "Body": {body}, "MessageSid": {sid}}
	req := env.buildRequest(t, "/api/v1/integrations/twilio"+MessagePath, url.Values{}, form, true, false)

	rec := httptest.NewRecorder()
	require.NoError(t, env.handler.VerifyMessageMiddleware(env.handler.HandleMessage)(rec, req))

	return rec
}

func inboundThreads(t *testing.T, inbox *support.Service) []*models.SupportThread {
	t.Helper()

	threads, err := inbox.ListThreads(context.Background(), models.ListSupportThreadsFilter{})
	require.NoError(t, err)

	return threads
}

// TestInboundSMSIsCaptured — before this route existed, an SMS reply died at
// Twilio with no trace anywhere in SolidPing.
func TestInboundSMSIsCaptured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env, inbox := setupInboundSMSEnv(t)

	rec := postInboundSMS(t, env, "the alert was a false alarm", "SM1")
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), "<Response></Response>")

	threads := inboundThreads(t, inbox)
	r.Len(threads, 1)
	r.Equal(models.SupportChannelSMS, threads[0].Channel)
	r.Equal(testInboundNumber, threads[0].ChannelIdentity)

	messages, err := inbox.ListMessages(context.Background(), threads[0].UID, 0)
	r.NoError(err)
	r.Len(messages, 1)
	r.Equal("the alert was a false alarm", messages[0].Body)
}

// TestCarrierKeywordsAreNotCaptured is the assertion the spec calls out by name.
//
// STOP/START/HELP carry LEGAL meaning and are handled by Twilio's platform-level
// Advanced Opt-Out. Filing them as support tickets would bury a real opt-out in
// an inbox nobody is obliged to read, while implying we handled something we did
// not.
func TestCarrierKeywordsAreNotCaptured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env, inbox := setupInboundSMSEnv(t)

	for i, keyword := range []string{
		"STOP", "stop", " Stop ", "STOPALL", "UNSUBSCRIBE", "CANCEL", "END", "QUIT",
		"START", "UNSTOP", "YES", "HELP", "help", "INFO",
	} {
		rec := postInboundSMS(t, env, keyword, "SM-KW-"+string(rune('a'+i)))
		r.Equal(http.StatusOK, rec.Code, "%q must still be answered normally", keyword)
	}

	r.Empty(inboundThreads(t, inbox),
		"carrier keywords must never become support threads")

	// POSITIVE CONTROL, on the same handler and the same sender: a message that
	// merely CONTAINS a keyword is a support message, not an opt-out. Without
	// this the assertions above would pass on a handler that captures nothing.
	rec := postInboundSMS(t, env, "please stop paging me at 3am", "SM-REAL")
	r.Equal(http.StatusOK, rec.Code)

	threads := inboundThreads(t, inbox)
	r.Len(threads, 1)

	messages, err := inbox.ListMessages(context.Background(), threads[0].UID, 0)
	r.NoError(err)
	r.Len(messages, 1)
	r.Equal("please stop paging me at 3am", messages[0].Body)
}

// TestIsCarrierKeyword pins the exact matching rule: the keyword alone, never a
// sentence containing it.
func TestIsCarrierKeyword(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, keyword := range []string{"STOP", "stop", "  Stop\n", "HELP", "start"} {
		r.True(IsCarrierKeyword(keyword), "%q", keyword)
	}

	for _, message := range []string{
		"please stop paging me", "help! the api is down", "", "stop it now", "starting to worry",
	} {
		r.False(IsCarrierKeyword(message), "%q", message)
	}
}

// TestInboundSMSRejectsUnsignedRequests — the route is public; the signature is
// the only gate.
func TestInboundSMSRejectsUnsignedRequests(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env, inbox := setupInboundSMSEnv(t)

	form := url.Values{"From": {testInboundNumber}, "Body": {"hi"}, "MessageSid": {"SM-BAD"}}

	unsigned := env.buildRequest(t, "/api/v1/integrations/twilio"+MessagePath, url.Values{}, form, false, false)
	rec := httptest.NewRecorder()
	r.NoError(env.handler.VerifyMessageMiddleware(env.handler.HandleMessage)(rec, unsigned))
	r.Equal(http.StatusForbidden, rec.Code)

	tampered := env.buildRequest(t, "/api/v1/integrations/twilio"+MessagePath, url.Values{}, form, true, true)
	rec = httptest.NewRecorder()
	r.NoError(env.handler.VerifyMessageMiddleware(env.handler.HandleMessage)(rec, tampered))
	r.Equal(http.StatusForbidden, rec.Code)

	r.Empty(inboundThreads(t, inbox))
}

// TestInboundSMSRejectedWhenInstanceDoesNotSendTwilioSMS — an OVH instance never
// receives a Twilio inbound webhook, so a request claiming to be one is refused
// rather than validated against an unrelated account.
func TestInboundSMSRejectedWhenInstanceDoesNotSendTwilioSMS(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env, _ := setupInboundSMSEnv(t)
	env.cfg.SMS.Provider = config.SMSProviderOVH

	rec := postInboundSMS(t, env, "hi", "SM-OVH")
	r.Equal(http.StatusForbidden, rec.Code)
}
