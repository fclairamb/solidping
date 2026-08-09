package twilio

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" // Twilio mandates HMAC-SHA1; the test mirrors that.
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// signForTest independently reproduces Twilio's webhook-signature algorithm so
// the ValidateSignature assertions aren't tautological against the same code.
func signForTest(authToken, fullURL string, params url.Values) string {
	var builder strings.Builder
	builder.WriteString(fullURL)

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range params[k] {
			builder.WriteString(k)
			builder.WriteString(v)
		}
	}

	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(builder.String()))

	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// capturingServer records the last request's path, form values, and basic-auth
// so the test goroutine can assert after the client call returns.
type capturingServer struct {
	mu       sync.Mutex
	path     string
	form     url.Values
	authUser string
	authPass string
	status   int
	body     string
}

func newCapturingServer(t *testing.T, status int, body string) (*capturingServer, *Client) {
	t.Helper()

	cs := &capturingServer{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		user, pass, _ := r.BasicAuth()

		cs.mu.Lock()
		cs.path = r.URL.Path
		cs.form = r.PostForm
		cs.authUser = user
		cs.authPass = pass
		cs.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cs.status)
		_, _ = w.Write([]byte(cs.body))
	}))
	t.Cleanup(srv.Close)

	return cs, NewClientWithBaseURL("AC00000000000000000000000000000001", "tok-secret", srv.URL)
}

func TestSendSMS_PostsFormFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cs, client := newCapturingServer(t, http.StatusCreated, `{"sid":"SM123","status":"queued"}`)

	res, err := client.SendSMS(context.Background(), &SendSMSParams{
		To:             "+15551230000",
		From:           "+15559990000",
		Body:           "hello",
		StatusCallback: "https://example.com/cb",
	})
	r.NoError(err)
	r.Equal("SM123", res.SID)
	r.Equal("queued", res.Status)

	cs.mu.Lock()
	defer cs.mu.Unlock()
	r.Equal("/2010-04-01/Accounts/AC00000000000000000000000000000001/Messages.json", cs.path)
	r.Equal("+15551230000", cs.form.Get("To"))
	r.Equal("+15559990000", cs.form.Get("From"))
	r.Equal("hello", cs.form.Get("Body"))
	r.Equal("https://example.com/cb", cs.form.Get("StatusCallback"))
	r.Empty(cs.form.Get("MessagingServiceSid"))
	// Basic auth carries account SID + auth token.
	r.Equal("AC00000000000000000000000000000001", cs.authUser)
	r.Equal("tok-secret", cs.authPass)
}

func TestSendSMS_MessagingService(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cs, client := newCapturingServer(t, http.StatusCreated, `{"sid":"SM9"}`)

	_, err := client.SendSMS(context.Background(), &SendSMSParams{
		To:                  "+15551230000",
		MessagingServiceSID: "MG123",
		Body:                "hi",
	})
	r.NoError(err)

	cs.mu.Lock()
	defer cs.mu.Unlock()
	r.Equal("MG123", cs.form.Get("MessagingServiceSid"))
	r.Empty(cs.form.Get("From"))
}

func TestSendSMS_MissingSender(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	client := NewClientWithBaseURL("AC1", "tok", "http://127.0.0.1:0")
	_, err := client.SendSMS(context.Background(), &SendSMSParams{To: "+15551230000", Body: "x"})
	r.ErrorIs(err, ErrMissingSender)
}

func TestSendSMS_APIError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, client := newCapturingServer(t, http.StatusBadRequest, `{"code":21211,"message":"Invalid 'To'"}`)

	_, err := client.SendSMS(context.Background(), &SendSMSParams{
		To: "+1", From: "+15559990000", Body: "x",
	})
	r.ErrorIs(err, ErrRequestFailed)
	r.Contains(err.Error(), "Invalid 'To'")
}

func TestCreateCall_PostsFormFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cs, client := newCapturingServer(t, http.StatusCreated, `{"sid":"CA123","status":"queued"}`)

	res, err := client.CreateCall(context.Background(), CreateCallParams{
		To:       "+15551230000",
		From:     "+15559990000",
		TwiMLURL: "https://example.com/voice",
	})
	r.NoError(err)
	r.Equal("CA123", res.SID)

	cs.mu.Lock()
	defer cs.mu.Unlock()
	r.Equal("/2010-04-01/Accounts/AC00000000000000000000000000000001/Calls.json", cs.path)
	r.Equal("+15551230000", cs.form.Get("To"))
	r.Equal("+15559990000", cs.form.Get("From"))
	r.Equal("https://example.com/voice", cs.form.Get("Url"))
}

func TestValidE164(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(ValidE164("+15551234567"))
	r.True(ValidE164("+33612345678"))
	r.False(ValidE164("15551234567"))  // no +
	r.False(ValidE164("+0123456789"))  // leading zero
	r.False(ValidE164("+123"))         // too short
	r.False(ValidE164("+1555123456a")) // non-digit
	r.False(ValidE164(""))
}

func TestValidAccountSID(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(ValidAccountSID("AC00000000000000000000000000000001"))
	r.False(ValidAccountSID("SK00000000000000000000000000000001"))
	r.False(ValidAccountSID("AC123"))
	r.False(ValidAccountSID(""))
}

func TestValidateSignature(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	authToken := "12345"
	fullURL := "https://example.com/api/v1/integrations/twilio/voice"
	params := url.Values{
		"CallSid": {"CA1"},
		"Digits":  {"4"},
	}

	// Compute the expected signature the same way the validator does, then
	// confirm it validates and a tampered signature does not.
	valid := signForTest(authToken, fullURL, params)
	r.True(ValidateSignature(authToken, fullURL, params, valid))
	r.False(ValidateSignature(authToken, fullURL, params, valid+"tamper"))
	r.False(ValidateSignature(authToken, fullURL, params, ""))
	r.False(ValidateSignature("wrong-token", fullURL, params, valid))
}

// TestBaseURLForRegion pins the mapping contract: "" and "us1" both resolve
// to DefaultBaseURL, and any other (already-validated) region token maps to
// the api.<region>.twilio.com host.
func TestBaseURLForRegion(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(DefaultBaseURL, BaseURLForRegion(""))
	r.Equal(DefaultBaseURL, BaseURLForRegion("us1"))
	r.Equal("https://api.ie1.twilio.com", BaseURLForRegion("ie1"))
	r.Equal("https://api.au1.twilio.com", BaseURLForRegion("au1"))
	r.Equal("https://api.br2.twilio.com", BaseURLForRegion("br2")) // unknown-but-well-formed: not an allowlist
}

// TestValidRegion proves the format check is the SSRF guard: any token that
// isn't strictly [a-z]{2}[0-9]+ is rejected, even though there is no
// enumerated allowlist of known regions.
func TestValidRegion(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(ValidRegion(""))
	r.True(ValidRegion("us1"))
	r.True(ValidRegion("ie1"))
	r.True(ValidRegion("au1"))
	r.True(ValidRegion("br2")) // unknown-but-well-formed region must be accepted

	r.False(ValidRegion("evil.com"))
	r.False(ValidRegion("../x"))
	r.False(ValidRegion("us1/x"))
	r.False(ValidRegion("US1")) // uppercase not allowed
	r.False(ValidRegion("us"))  // no digits
	r.False(ValidRegion("1us")) // digits first
	r.False(ValidRegion("us1 "))
	r.False(ValidRegion(" us1"))
}

// newAccountFetchServer returns a fake Twilio "Accounts/{sid}.json" endpoint
// that responds with the given status, and records the request path and
// basic-auth credentials it received.
func newAccountFetchServer(t *testing.T, status int) (*capturingServer, *httptest.Server) {
	t.Helper()

	cs := &capturingServer{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()

		cs.mu.Lock()
		cs.path = r.URL.Path
		cs.authUser = user
		cs.authPass = pass
		cs.mu.Unlock()

		w.WriteHeader(cs.status)
	}))
	t.Cleanup(srv.Close)

	return cs, srv
}

func TestVerifyCredentials_Accepted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cs, srv := newAccountFetchServer(t, http.StatusOK)

	err := VerifyCredentials(t.Context(), "AC00000000000000000000000000000001", "tok-secret", srv.URL)
	r.NoError(err)

	cs.mu.Lock()
	defer cs.mu.Unlock()
	r.Equal("/2010-04-01/Accounts/AC00000000000000000000000000000001.json", cs.path)
	r.Equal("AC00000000000000000000000000000001", cs.authUser)
	r.Equal("tok-secret", cs.authPass)
}

func TestVerifyCredentials_Rejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, srv401 := newAccountFetchServer(t, http.StatusUnauthorized)
	err := VerifyCredentials(t.Context(), "AC00000000000000000000000000000001", "wrong", srv401.URL)
	r.ErrorIs(err, ErrCredentialsRejected)

	_, srv403 := newAccountFetchServer(t, http.StatusForbidden)
	err = VerifyCredentials(t.Context(), "AC00000000000000000000000000000001", "wrong", srv403.URL)
	r.ErrorIs(err, ErrCredentialsRejected)
}

func TestVerifyCredentials_Unverifiable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Unexpected status (e.g. a 5xx) is "could not verify", not "rejected".
	_, srv500 := newAccountFetchServer(t, http.StatusInternalServerError)
	err := VerifyCredentials(t.Context(), "AC00000000000000000000000000000001", "tok", srv500.URL)
	r.ErrorIs(err, ErrCredentialsUnverifiable)
	r.NotErrorIs(err, ErrCredentialsRejected)

	// Unreachable host (network error).
	err = VerifyCredentials(t.Context(), "AC00000000000000000000000000000001", "tok", "http://127.0.0.1:1")
	r.ErrorIs(err, ErrCredentialsUnverifiable)
}
