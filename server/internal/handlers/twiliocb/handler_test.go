package twiliocb

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" // Twilio mandates HMAC-SHA1; the test reproduces it.
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
)

const testAuthToken = "twilio-auth-token"

// mockAcker records AcknowledgeIncidentFromPhone calls.
type mockAcker struct {
	orgUID      string
	incidentUID string
	phone       string
	called      bool
}

func (m *mockAcker) AcknowledgeIncidentFromPhone(
	_ context.Context, orgUID, incidentUID, phone string,
) (*models.Incident, error) {
	m.called = true
	m.orgUID = orgUID
	m.incidentUID = incidentUID
	m.phone = phone

	return &models.Incident{UID: incidentUID}, nil
}

type cbEnv struct {
	handler     *Handler
	db          *sqlite.Service
	acker       *mockAcker
	integration *models.Integration
	incident    *models.Incident
	cfg         *config.Config
}

func setupCallbackEnv(t *testing.T) *cbEnv {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("cb-org", "CB Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	checkName := "Payments API"
	check := &models.Check{UID: "check-1", OrganizationUID: org.UID, Name: &checkName, Type: "http"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	incident := &models.Incident{
		UID: "incident-1", OrganizationUID: org.UID, CheckUID: check.UID,
		State: models.IncidentStateActive, StartedAt: time.Now().Add(-2 * time.Minute),
	}
	require.NoError(t, dbSvc.CreateIncident(ctx, incident))

	integration := models.NewIntegration(org.UID, models.ConnectionTypeTwilio, "twilio")
	integration.Enabled = true
	integration.Settings = models.JSONMap{
		"account_sid": "AC00000000000000000000000000000001",
		"auth_token":  testAuthToken,
		"from_number": "+15559990000",
	}
	require.NoError(t, dbSvc.CreateChannel(ctx, integration))

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.Server.BaseURL = "https://app.example.com"

	acker := &mockAcker{}

	return &cbEnv{
		handler:     NewHandler(dbSvc, nil, cfg, acker),
		db:          dbSvc,
		acker:       acker,
		integration: integration,
		incident:    incident,
		cfg:         cfg,
	}
}

func (e *cbEnv) ackToken(exp time.Time) string {
	return incidentlinks.Sign([]byte(e.cfg.Auth.JWTSecret), e.incident.UID, "+15551230000", exp)
}

// signTwilio reproduces Twilio's webhook-signature algorithm.
func signTwilio(authToken, fullURL string, form url.Values) string {
	var b strings.Builder
	b.WriteString(fullURL)

	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range form[k] {
			b.WriteString(k)
			b.WriteString(v)
		}
	}

	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(b.String()))

	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// buildRequest constructs a signed (unless sign=false / badSig) POST request to
// the given twilio callback path with the given query + body params.
func (e *cbEnv) buildRequest(
	t *testing.T, path string, query, body url.Values, signIt, badSig bool,
) *http.Request {
	t.Helper()

	target := path + "?" + query.Encode()
	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost, target, strings.NewReader(body.Encode()),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	fullURL := strings.TrimRight(e.cfg.Server.BaseURL, "/") + req.URL.RequestURI()

	if signIt {
		sig := signTwilio(testAuthToken, fullURL, body)
		if badSig {
			sig += "tamper"
		}
		req.Header.Set("X-Twilio-Signature", sig)
	}

	return req
}

func (e *cbEnv) voiceQuery(token string) url.Values {
	return url.Values{"cid": {e.integration.UID}, "iid": {e.incident.UID}, "token": {token}}
}

func TestVoice_ValidSignatureReturnsGather(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	token := env.ackToken(time.Now().Add(time.Hour))
	body := url.Values{"CallSid": {"CA1"}}
	req := env.buildRequest(t, "/api/v1/integrations/twilio/voice", env.voiceQuery(token), body, true, false)

	rec := httptest.NewRecorder()
	require.NoError(t, env.handler.VerifyVoiceMiddleware(env.handler.HandleVoice)(rec, req))

	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), "<Gather")
	r.Contains(rec.Body.String(), "Payments API is down")
	r.Contains(rec.Body.String(), "Press 4 to acknowledge")
}

func TestVoice_InvalidSignature403(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	token := env.ackToken(time.Now().Add(time.Hour))
	req := env.buildRequest(t, "/api/v1/integrations/twilio/voice", env.voiceQuery(token), url.Values{}, true, true)

	rec := httptest.NewRecorder()
	require.NoError(t, env.handler.VerifyVoiceMiddleware(env.handler.HandleVoice)(rec, req))

	r.Equal(http.StatusForbidden, rec.Code)
	r.Empty(rec.Body.String())
}

func TestVoice_MissingSignature403(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	token := env.ackToken(time.Now().Add(time.Hour))
	req := env.buildRequest(t, "/api/v1/integrations/twilio/voice", env.voiceQuery(token), url.Values{}, false, false)

	rec := httptest.NewRecorder()
	require.NoError(t, env.handler.VerifyVoiceMiddleware(env.handler.HandleVoice)(rec, req))

	r.Equal(http.StatusForbidden, rec.Code)
}

func TestVoice_ExpiredToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	token := env.ackToken(time.Now().Add(-time.Hour)) // expired
	req := env.buildRequest(t, "/api/v1/integrations/twilio/voice", env.voiceQuery(token), url.Values{}, true, false)

	rec := httptest.NewRecorder()
	require.NoError(t, env.handler.VerifyVoiceMiddleware(env.handler.HandleVoice)(rec, req))

	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), "no longer be acknowledged")
	r.NotContains(rec.Body.String(), "<Gather")
}

func TestGather_Digit4Acks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	token := env.ackToken(time.Now().Add(time.Hour))
	body := url.Values{"Digits": {"4"}, "From": {"+15551230000"}}
	req := env.buildRequest(t, "/api/v1/integrations/twilio/voice/gather", env.voiceQuery(token), body, true, false)

	rec := httptest.NewRecorder()
	require.NoError(t, env.handler.VerifyVoiceMiddleware(env.handler.HandleGather)(rec, req))

	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), "Acknowledged")
	r.True(env.acker.called, "AcknowledgeIncidentFromPhone must be invoked")
	r.Equal(env.incident.UID, env.acker.incidentUID)
	r.Equal("+15551230000", env.acker.phone)
	r.Equal(env.integration.OrganizationUID, env.acker.orgUID)
}

func TestGather_OtherDigitReprompts(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCallbackEnv(t)
	token := env.ackToken(time.Now().Add(time.Hour))
	body := url.Values{"Digits": {"9"}, "From": {"+15551230000"}}
	req := env.buildRequest(t, "/api/v1/integrations/twilio/voice/gather", env.voiceQuery(token), body, true, false)

	rec := httptest.NewRecorder()
	require.NoError(t, env.handler.VerifyVoiceMiddleware(env.handler.HandleGather)(rec, req))

	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), "<Gather")
	r.False(env.acker.called, "a non-4 digit must not acknowledge")
}
