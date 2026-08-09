package notifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// twilioFake captures every SMS/call request body posted by the sender.
type twilioFake struct {
	mu    sync.Mutex
	forms []url.Values
	sid   string
}

func newTwilioFake(t *testing.T) (*twilioFake, string) {
	t.Helper()

	f := &twilioFake{sid: "SM_test"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.forms = append(f.forms, r.PostForm)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"` + f.sid + `","status":"queued"}`))
	}))
	t.Cleanup(srv.Close)

	return f, srv.URL
}

func twilioPayload(event string, settings models.JSONMap) *Payload {
	name := "API health"

	return &Payload{
		EventType: event,
		Incident: &models.Incident{
			UID:       "018e4a2b-incident",
			StartedAt: time.Now().Add(-2 * time.Minute),
		},
		Check:      &models.Check{UID: "chk-1", Name: &name, Type: "http"},
		OrgSlug:    "acme",
		AppBaseURL: "https://app.example.com",
		Integration: &models.Integration{
			UID: "chan-1", OrganizationUID: "org-1",
			Type: models.ConnectionTypeTwilio, Settings: settings,
		},
	}
}

func twilioJobCtx() *jobdef.JobContext {
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.Server.BaseURL = "https://app.example.com"

	return &jobdef.JobContext{AppConfig: cfg}
}

func fullTwilioSettings() models.JSONMap {
	return models.JSONMap{
		"account_sid": "AC00000000000000000000000000000001",
		"auth_token":  "tok",
		"from_number": "+15559990000",
		"to_numbers":  []any{"+15551230000", "+15551230001"},
	}
}

func TestTwilioSender_SendsToAllRecipients(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, baseURL := newTwilioFake(t)
	sender := &TwilioSender{BaseURL: baseURL}

	payload := twilioPayload(eventTypeIncidentCreated, fullTwilioSettings())
	err := sender.Send(context.Background(), twilioJobCtx(), payload)
	r.NoError(err)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	r.Len(fake.forms, 2)
	tos := []string{fake.forms[0].Get("To"), fake.forms[1].Get("To")}
	r.ElementsMatch([]string{"+15551230000", "+15551230001"}, tos)
	r.Equal("+15559990000", fake.forms[0].Get("From"))
	r.Contains(fake.forms[0].Get("Body"), "is DOWN")
	r.Contains(fake.forms[0].Get("Body"), "Ack:")
	r.Contains(fake.forms[0].Get("StatusCallback"), "/api/v1/integrations/twilio/status?cid=chan-1")
	// MessageID surfaces the Twilio SID for the audit row.
	r.Equal("SM_test", payload.MessageID)
}

func TestTwilioSender_ResolvedHasNoAckLink(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, baseURL := newTwilioFake(t)
	sender := &TwilioSender{BaseURL: baseURL}

	settings := fullTwilioSettings()
	settings["to_numbers"] = []any{"+15551230000"}

	err := sender.Send(context.Background(), twilioJobCtx(), twilioPayload(eventTypeIncidentResolved, settings))
	r.NoError(err)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	r.Len(fake.forms, 1)
	r.Contains(fake.forms[0].Get("Body"), "RECOVERED")
	r.NotContains(fake.forms[0].Get("Body"), "Ack:")
}

func TestTwilioSender_EmptyRecipientsErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, baseURL := newTwilioFake(t)
	sender := &TwilioSender{BaseURL: baseURL}

	settings := fullTwilioSettings()
	delete(settings, "to_numbers")

	err := sender.Send(context.Background(), twilioJobCtx(), twilioPayload(eventTypeIncidentCreated, settings))
	r.ErrorIs(err, ErrTwilioNoRecipients)
}

// TestTwilioSender_ResolveBaseURL pins the documented precedence: the test
// override (BaseURL) wins when set, regardless of region, and the
// connection's region decides only when BaseURL is empty — which is what
// keeps a connection with no region behaving exactly as before this field
// existed (both resolve to twilio.DefaultBaseURL).
func TestTwilioSender_ResolveBaseURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// BaseURL set: wins over any region, even a non-default one.
	overridden := &TwilioSender{BaseURL: "http://fake.test"}
	r.Equal("http://fake.test", overridden.resolveBaseURL(""))
	r.Equal("http://fake.test", overridden.resolveBaseURL("ie1"))

	// BaseURL empty: falls through to the region.
	prod := &TwilioSender{}
	r.Equal("https://api.twilio.com", prod.resolveBaseURL(""), "no region behaves exactly as before this field existed")
	r.Equal("https://api.twilio.com", prod.resolveBaseURL("us1"))
	r.Equal("https://api.ie1.twilio.com", prod.resolveBaseURL("ie1"))
}

func TestTwilioSender_MissingCredentialsErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, baseURL := newTwilioFake(t)
	sender := &TwilioSender{BaseURL: baseURL}

	settings := models.JSONMap{"account_sid": "AC1", "to_numbers": []any{"+15551230000"}}
	err := sender.Send(context.Background(), twilioJobCtx(), twilioPayload(eventTypeIncidentCreated, settings))
	r.ErrorIs(err, ErrTwilioNotConfigured)
}
