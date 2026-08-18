package twiliocb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/sms"
)

// The two instance auth tokens are deliberately DIFFERENT here. On most
// deployments one Twilio account serves both capabilities and the two tokens
// are the same string, which makes reading the wrong one invisible — a test on
// such a deployment passes whichever token the code picks.
const (
	instanceSMSToken   = "instance-sms-token"
	instanceVoiceToken = "instance-voice-token"
)

const statusPath = "/api/v1/integrations/twilio/status"

// enableInstanceSMS turns on server-provided SMS with the given provider.
func (e *cbEnv) enableInstanceSMS(provider string) {
	e.cfg.SMS = config.SMSConfig{
		Enabled:  true,
		Provider: provider,
		Sender:   "+15550001111",
		Twilio: config.SMSTwilioConfig{
			AccountSID: "AC00000000000000000000000000000009",
			AuthToken:  instanceSMSToken,
		},
		OVH: config.SMSOVHConfig{
			Endpoint: "ovh-eu", ApplicationKey: "ak", ApplicationSecret: "as",
			ConsumerKey: "ck", ServiceName: "sms-ab12345-1",
		},
	}
}

// enableInstanceVoice turns on server-provided voice.
func (e *cbEnv) enableInstanceVoice() {
	e.cfg.Voice = config.VoiceConfig{
		Enabled:    true,
		FromNumber: "+15550002222",
		Twilio: config.SMSTwilioConfig{
			AccountSID: "AC00000000000000000000000000000009",
			AuthToken:  instanceVoiceToken,
		},
	}
}

// seedSentNotification records a phone notification audit row carrying
// messageID, exactly as auditPhoneSend does after a send.
func (e *cbEnv) seedSentNotification(t *testing.T, messageID, channel string) string {
	t.Helper()

	ctx := context.Background()

	user := models.NewUser("oncall+" + messageID + "@example.com")
	require.NoError(t, e.db.CreateUser(ctx, user))

	repeat := 0
	n := models.NewIncidentNotificationForUser(
		e.incident.OrganizationUID, e.incident.UID,
		string(models.EventTypeIncidentEscalated),
		models.IncidentNotificationSourceEscalationUser,
		user.UID, channel, nil, &repeat,
	)
	require.NoError(t, e.db.CreateIncidentNotification(ctx, n))
	require.NoError(t, e.db.MarkIncidentNotificationSentByUID(ctx, n.UID, time.Now(), messageID))

	return n.UID
}

// deliveryDetailsOf reads back the stored delivery details of one audit row.
func (e *cbEnv) deliveryDetailsOf(t *testing.T, uid string) *models.DeliveryDetails {
	t.Helper()

	rows, err := e.db.ListIncidentNotifications(
		context.Background(), e.incident.OrganizationUID,
		db.ListIncidentNotificationsFilter{IncidentUID: e.incident.UID},
	)
	require.NoError(t, err)

	for _, row := range rows {
		if row.UID == uid {
			return row.DeliveryDetails
		}
	}

	t.Fatalf("notification row %s not found", uid)

	return nil
}

// statusRequest builds a status callback POST signed with signingToken. An
// empty signingToken sends no signature at all; tamperOID rewrites `oid` AFTER
// signing, to prove the org id is covered by the signature.
func (e *cbEnv) statusRequest(
	t *testing.T, query, body url.Values, signingToken, tamperOID string,
) *http.Request {
	t.Helper()

	target := statusPath + "?" + query.Encode()
	fullURL := strings.TrimRight(e.cfg.Server.BaseURL, "/") + target

	if signingToken != "" {
		sig := signTwilio(signingToken, fullURL, body)

		if tamperOID != "" {
			tampered := query
			tampered.Set("oid", tamperOID)
			target = statusPath + "?" + tampered.Encode()
		}

		req := httptest.NewRequestWithContext(
			context.Background(), http.MethodPost, target, strings.NewReader(body.Encode()),
		)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Twilio-Signature", sig)

		return req
	}

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost, target, strings.NewReader(body.Encode()),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return req
}

func (e *cbEnv) postStatus(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	require.NoError(t, e.handler.VerifyStatusMiddleware(e.handler.HandleStatus)(rec, req))

	return rec
}

func (e *cbEnv) instanceQuery() url.Values {
	return url.Values{
		"cid": {sms.InstanceCallbackID},
		"oid": {e.incident.OrganizationUID},
	}
}

// TestInstanceSMSStatusRoundTrip is the end-to-end proof of the fix, and at the
// same time the positive control for the first broken configuration: Twilio SMS
// with SP_VOICE_ENABLED=false. Before the fix the sentinel branch gated on
// Voice.Active() and validated with the VOICE token, so this perfectly valid
// callback was rejected with a bare 403.
func TestInstanceSMSStatusRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	env.enableInstanceSMS(config.SMSProviderTwilio)
	// Voice deliberately left disabled — SP_VOICE_ENABLED=false.
	r.False(env.cfg.Voice.Active())

	uid := env.seedSentNotification(t, "SM100", "sms")
	r.Nil(env.deliveryDetailsOf(t, uid), "no delivery state before the callback")

	body := url.Values{"MessageSid": {"SM100"}, "MessageStatus": {"delivered"}}
	rec := env.postStatus(t, env.statusRequest(t, env.instanceQuery(), body, instanceSMSToken, ""))

	r.Equal(http.StatusNoContent, rec.Code)

	details := env.deliveryDetailsOf(t, uid)
	r.NotNil(details, "the callback must land on the notification row")
	r.Contains(details.ResponseBody, "delivered")
}

// TestInstanceSMSStatusWrongSignatureWritesNothing is the negative half of the
// round trip: the same payload with a bad signature must be refused with a BARE
// 403 and must not touch the row.
func TestInstanceSMSStatusWrongSignatureWritesNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	env.enableInstanceSMS(config.SMSProviderTwilio)

	uid := env.seedSentNotification(t, "SM101", "sms")

	body := url.Values{"MessageSid": {"SM101"}, "MessageStatus": {"delivered"}}
	req := env.statusRequest(t, env.instanceQuery(), body, "not-the-instance-token", "")
	rec := env.postStatus(t, req)

	r.Equal(http.StatusForbidden, rec.Code)
	r.Empty(rec.Body.String(), "a 403 must carry no detail an unauthenticated caller could probe with")
	r.Nil(env.deliveryDetailsOf(t, uid), "a rejected callback must write nothing")
}

// TestInstanceSMSStatusMissingSignature403 covers the unsigned request.
func TestInstanceSMSStatusMissingSignature403(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	env.enableInstanceSMS(config.SMSProviderTwilio)
	uid := env.seedSentNotification(t, "SM102", "sms")

	body := url.Values{"MessageSid": {"SM102"}, "MessageStatus": {"delivered"}}
	rec := env.postStatus(t, env.statusRequest(t, env.instanceQuery(), body, "", ""))

	r.Equal(http.StatusForbidden, rec.Code)
	r.Nil(env.deliveryDetailsOf(t, uid))
}

// TestInstanceStatusRejectsForgedOrg proves the `oid` inside the signed URL is
// what protects org scoping: rewriting it after signing breaks the signature.
func TestInstanceStatusRejectsForgedOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	env.enableInstanceSMS(config.SMSProviderTwilio)
	uid := env.seedSentNotification(t, "SM103", "sms")

	body := url.Values{"MessageSid": {"SM103"}, "MessageStatus": {"delivered"}}
	rec := env.postStatus(t, env.statusRequest(t, env.instanceQuery(), body, instanceSMSToken, "other-org"))

	r.Equal(http.StatusForbidden, rec.Code)
	r.Nil(env.deliveryDetailsOf(t, uid))
}

// TestInstanceStatusWriteIsScopedToTheSignedOrg pins that HandleStatus takes
// the org from the signed `oid` and never from the payload: a validly signed
// callback naming another org must not touch this org's row, even though the
// message id matches.
func TestInstanceStatusWriteIsScopedToTheSignedOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	env.enableInstanceSMS(config.SMSProviderTwilio)
	uid := env.seedSentNotification(t, "SM104", "sms")

	query := url.Values{"cid": {sms.InstanceCallbackID}, "oid": {"some-other-org"}}
	body := url.Values{"MessageSid": {"SM104"}, "MessageStatus": {"delivered"}}
	rec := env.postStatus(t, env.statusRequest(t, query, body, instanceSMSToken, ""))

	r.Equal(http.StatusNoContent, rec.Code, "the signature is valid, so the request is accepted")
	r.Nil(env.deliveryDetailsOf(t, uid),
		"but the write stays scoped to the org named in the signed url")
}

// TestInstanceVoiceStatusUsesTheVoiceToken is the voice half of the shared
// endpoint: a CallSid receipt is validated with the VOICE credentials.
func TestInstanceVoiceStatusUsesTheVoiceToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	env.enableInstanceSMS(config.SMSProviderTwilio)
	env.enableInstanceVoice()

	uid := env.seedSentNotification(t, "CA200", "voice")

	body := url.Values{"CallSid": {"CA200"}, "CallStatus": {"completed"}}
	rec := env.postStatus(t, env.statusRequest(t, env.instanceQuery(), body, instanceVoiceToken, ""))

	r.Equal(http.StatusNoContent, rec.Code)
	details := env.deliveryDetailsOf(t, uid)
	r.NotNil(details)
	r.Contains(details.ResponseBody, "completed")
}

// TestStatusCrossCapabilityRejection is the reason the two tokens differ in
// this file: neither capability's token may validate the other's callback.
func TestStatusCrossCapabilityRejection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		body  url.Values
		token string
	}{
		{
			"a call receipt signed with the SMS token",
			url.Values{"CallSid": {"CA300"}, "CallStatus": {"completed"}},
			instanceSMSToken,
		},
		{
			"a message receipt signed with the voice token",
			url.Values{"MessageSid": {"SM300"}, "MessageStatus": {"delivered"}},
			instanceVoiceToken,
		},
		{
			"a payload naming neither capability",
			url.Values{"MessageStatus": {"delivered"}},
			instanceSMSToken,
		},
		{
			"a payload naming both, so the capability cannot be told",
			url.Values{"MessageSid": {"SM301"}, "CallSid": {"CA301"}, "MessageStatus": {"delivered"}},
			instanceSMSToken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			env := setupCallbackEnv(t)
			env.enableInstanceSMS(config.SMSProviderTwilio)
			env.enableInstanceVoice()

			rec := env.postStatus(t, env.statusRequest(t, env.instanceQuery(), tt.body, tt.token, ""))

			r.Equal(http.StatusForbidden, rec.Code)
			r.Empty(rec.Body.String())
		})
	}
}

// TestOVHSMSInstanceRejectsTwilioSMSStatus is the positive control for the
// second broken configuration: OVH for SMS, Twilio for voice. The SMS receipt
// must not be validated against the Twilio VOICE credentials — the instance
// never asks Twilio for one — while the voice callback keeps working.
func TestOVHSMSInstanceRejectsTwilioSMSStatus(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	env.enableInstanceSMS(config.SMSProviderOVH)
	env.enableInstanceVoice()
	r.True(env.cfg.SMS.Active())
	r.True(env.cfg.Voice.Active())

	smsUID := env.seedSentNotification(t, "SM400", "sms")

	// Even signed with the voice token — the only Twilio credential this
	// instance holds — an SMS status callback is refused.
	smsBody := url.Values{"MessageSid": {"SM400"}, "MessageStatus": {"delivered"}}
	rec := env.postStatus(t, env.statusRequest(t, env.instanceQuery(), smsBody, instanceVoiceToken, ""))
	r.Equal(http.StatusForbidden, rec.Code)
	r.Nil(env.deliveryDetailsOf(t, smsUID))

	// The voice callback on the same instance still works.
	callUID := env.seedSentNotification(t, "CA400", "voice")
	callBody := url.Values{"CallSid": {"CA400"}, "CallStatus": {"completed"}}
	rec = env.postStatus(t, env.statusRequest(t, env.instanceQuery(), callBody, instanceVoiceToken, ""))
	r.Equal(http.StatusNoContent, rec.Code)
	r.NotNil(env.deliveryDetailsOf(t, callUID))
}

// TestBringYourOwnStatusCallbackStillWorks is the regression control on the
// path that already worked: `cid` names the connection, whose own auth token
// validates the callback, and the org comes from the connection row.
func TestBringYourOwnStatusCallbackStillWorks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	// No instance credentials at all: the connection must be self-sufficient.
	uid := env.seedSentNotification(t, "SM500", "sms")

	query := url.Values{"cid": {env.integration.UID}}
	body := url.Values{"MessageSid": {"SM500"}, "MessageStatus": {"delivered"}}
	rec := env.postStatus(t, env.statusRequest(t, query, body, testAuthToken, ""))

	r.Equal(http.StatusNoContent, rec.Code)
	details := env.deliveryDetailsOf(t, uid)
	r.NotNil(details)
	r.Contains(details.ResponseBody, "delivered")

	// A wrong signature on the same path is still refused.
	uid2 := env.seedSentNotification(t, "SM501", "sms")
	body2 := url.Values{"MessageSid": {"SM501"}, "MessageStatus": {"delivered"}}
	rec = env.postStatus(t, env.statusRequest(t, query, body2, "wrong-token", ""))
	r.Equal(http.StatusForbidden, rec.Code)
	r.Nil(env.deliveryDetailsOf(t, uid2))
}

// TestBringYourOwnVoiceCallbackIgnoresInstanceCapabilities makes the point that
// the capability gate only ever applies to the instance sentinel: a per-org
// connection callback works with no instance SMS or voice configured.
func TestBringYourOwnVoiceCallbackIgnoresInstanceCapabilities(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	uid := env.seedSentNotification(t, "CA500", "voice")

	query := url.Values{"cid": {env.integration.UID}}
	body := url.Values{"CallSid": {"CA500"}, "CallStatus": {"completed"}}
	rec := env.postStatus(t, env.statusRequest(t, query, body, testAuthToken, ""))

	r.Equal(http.StatusNoContent, rec.Code)
	r.NotNil(env.deliveryDetailsOf(t, uid))
}
