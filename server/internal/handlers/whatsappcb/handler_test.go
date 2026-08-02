package whatsappcb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/integrations/whatsapp"
)

const (
	testAppSecret   = "meta-app-secret"
	testVerifyToken = "meta-verify-token"
	testMessageID   = "wamid.TESTID"
)

type wsEnv struct {
	handler  *Handler
	db       db.Service
	org      *models.Organization
	incident *models.Incident
	notifUID string
}

// setupEnv builds a handler over a fresh in-memory SQLite with one incident
// notification already "sent" under testMessageID, so status callbacks have
// something to land on. Each test gets its own DB — no shared global state.
func setupEnv(t *testing.T) *wsEnv {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("wa-org", "WA Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	checkName := "Payments API"
	check := &models.Check{UID: "check-1", OrganizationUID: org.UID, Name: &checkName, Type: "http"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	user := models.NewUser("oncall@example.com")
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	incident := &models.Incident{
		UID: "incident-1", OrganizationUID: org.UID, CheckUID: check.UID,
		State: models.IncidentStateActive, StartedAt: time.Now().Add(-time.Minute),
	}
	require.NoError(t, dbSvc.CreateIncident(ctx, incident))

	notif := models.NewIncidentNotificationForUser(
		org.UID, incident.UID, string(models.EventTypeIncidentEscalated),
		models.IncidentNotificationSourceEscalationUser, user.UID, "whatsapp",
		nil, nil,
	)
	require.NoError(t, dbSvc.CreateIncidentNotification(ctx, notif))
	require.NoError(t, dbSvc.MarkIncidentNotificationSentByUID(ctx, notif.UID, time.Now(), testMessageID))

	cfg := &config.Config{}
	cfg.WhatsApp = config.WhatsAppConfig{
		Enabled:            true,
		AccessToken:        "tok",
		PhoneNumberID:      "PNID",
		AppSecret:          testAppSecret,
		WebhookVerifyToken: testVerifyToken,
	}

	return &wsEnv{
		handler: NewHandler(dbSvc, cfg), db: dbSvc,
		org: org, incident: incident, notifUID: notif.UID,
	}
}

func (e *wsEnv) deliveryBody(t *testing.T) string {
	t.Helper()

	row, err := e.db.GetOrgNotification(context.Background(), e.org.UID, e.notifUID)
	require.NoError(t, err)
	require.NotNil(t, row)

	if row.DeliveryDetails == nil {
		return ""
	}

	return row.DeliveryDetails.ResponseBody
}

func statusBody(id, status string) string {
	return `{"object":"whatsapp_business_account","entry":[{"id":"WABA","changes":[{"field":"messages",` +
		`"value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"PNID"},` +
		`"statuses":[{"id":"` + id + `","status":"` + status + `","timestamp":"1700000000",` +
		`"recipient_id":"33612345678"}]}}]}]}`
}

// ---------------------------------------------------------------------------
// GET handshake
// ---------------------------------------------------------------------------

func TestHandleVerify(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		query      string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "valid handshake echoes the challenge",
			query:      "hub.mode=subscribe&hub.verify_token=" + testVerifyToken + "&hub.challenge=1158201444",
			wantStatus: http.StatusOK,
			wantBody:   "1158201444",
		},
		{
			name:       "wrong token is rejected",
			query:      "hub.mode=subscribe&hub.verify_token=nope&hub.challenge=1158201444",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "missing token is rejected",
			query:      "hub.mode=subscribe&hub.challenge=1158201444",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "wrong mode is rejected",
			query:      "hub.mode=unsubscribe&hub.verify_token=" + testVerifyToken + "&hub.challenge=1158201444",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "missing challenge is rejected",
			query:      "hub.mode=subscribe&hub.verify_token=" + testVerifyToken,
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := setupEnv(t)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/whatsapp/webhook?"+tc.query, nil)
			rec := httptest.NewRecorder()

			r.NoError(env.handler.HandleVerify(rec, req))
			r.Equal(tc.wantStatus, rec.Code)

			if tc.wantBody != "" {
				r.Equal(tc.wantBody, rec.Body.String())
			} else {
				r.Empty(rec.Body.String())
			}
		})
	}
}

// TestHandleVerify_NoTokenConfigured proves the handshake fails closed on an
// instance that has not configured a verify token — an empty configured token
// must never match an empty supplied one.
func TestHandleVerify_NoTokenConfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	env.handler.cfg.WhatsApp.WebhookVerifyToken = ""

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/integrations/whatsapp/webhook?hub.mode=subscribe&hub.verify_token=&hub.challenge=abc", nil)
	rec := httptest.NewRecorder()

	r.NoError(env.handler.HandleVerify(rec, req))
	r.Equal(http.StatusForbidden, rec.Code)
}

// ---------------------------------------------------------------------------
// POST signature validation — the security boundary
// ---------------------------------------------------------------------------

func TestHandleEvent_RejectsBadSignature(t *testing.T) {
	t.Parallel()

	body := statusBody(testMessageID, whatsapp.StatusDelivered)

	cases := []struct {
		name      string
		signature string
	}{
		{name: "missing signature", signature: ""},
		{name: "wrong secret", signature: whatsapp.Sign("other-secret", []byte(body))},
		{name: "no algorithm prefix", signature: strings.TrimPrefix(whatsapp.Sign(testAppSecret, []byte(body)), "sha256=")},
		{name: "not hex", signature: "sha256=zzzz"},
		{name: "signature of a different body", signature: whatsapp.Sign(testAppSecret, []byte(`{"object":"other"}`))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := setupEnv(t)

			req := httptest.NewRequest(http.MethodPost,
				"/api/v1/integrations/whatsapp/webhook", strings.NewReader(body))
			if tc.signature != "" {
				req.Header.Set("X-Hub-Signature-256", tc.signature)
			}

			rec := httptest.NewRecorder()
			r.NoError(env.handler.HandleEvent(rec, req))
			r.Equal(http.StatusForbidden, rec.Code)
			r.Empty(rec.Body.String())

			// Positive control: the rejected payload must NOT have touched the
			// delivery record. Without this the test could pass on a handler
			// that 403s after already applying the update.
			r.Empty(env.deliveryBody(t))
		})
	}
}

// TestHandleEvent_RejectsWhenNoAppSecretConfigured proves an instance without
// an app secret cannot be driven by an anonymous caller: with no secret there
// is no signature we could ever accept.
func TestHandleEvent_RejectsWhenNoAppSecretConfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	env.handler.cfg.WhatsApp.AppSecret = ""

	body := statusBody(testMessageID, whatsapp.StatusDelivered)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/integrations/whatsapp/webhook", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", whatsapp.Sign("", []byte(body)))

	rec := httptest.NewRecorder()
	r.NoError(env.handler.HandleEvent(rec, req))
	r.Equal(http.StatusForbidden, rec.Code)
	r.Empty(env.deliveryBody(t))
}

// ---------------------------------------------------------------------------
// POST status handling
// ---------------------------------------------------------------------------

func TestHandleEvent_UpdatesDeliveryRecord(t *testing.T) {
	t.Parallel()

	for _, status := range []string{
		whatsapp.StatusSent, whatsapp.StatusDelivered, whatsapp.StatusRead,
	} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := setupEnv(t)

			r.Empty(env.deliveryBody(t))

			body := statusBody(testMessageID, status)
			req := httptest.NewRequest(http.MethodPost,
				"/api/v1/integrations/whatsapp/webhook", strings.NewReader(body))
			req.Header.Set("X-Hub-Signature-256", whatsapp.Sign(testAppSecret, []byte(body)))

			rec := httptest.NewRecorder()
			r.NoError(env.handler.HandleEvent(rec, req))
			r.Equal(http.StatusNoContent, rec.Code)

			r.Equal("whatsapp status: "+status, env.deliveryBody(t))
		})
	}
}

func TestHandleEvent_FailedStatusCarriesReason(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	body := `{"object":"whatsapp_business_account","entry":[{"id":"WABA","changes":[{"field":"messages",` +
		`"value":{"statuses":[{"id":"` + testMessageID + `","status":"failed","timestamp":"1700000000",` +
		`"errors":[{"code":131026,"title":"Message undeliverable",` +
		`"error_data":{"details":"Receiver is not a WhatsApp user"}}]}]}}]}]}`

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/integrations/whatsapp/webhook", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", whatsapp.Sign(testAppSecret, []byte(body)))

	rec := httptest.NewRecorder()
	r.NoError(env.handler.HandleEvent(rec, req))
	r.Equal(http.StatusNoContent, rec.Code)

	detail := env.deliveryBody(t)
	r.Contains(detail, "failed")
	r.Contains(detail, "131026")
	r.Contains(detail, "Receiver is not a WhatsApp user")
}

// TestHandleEvent_UnknownMessageIDIsNoOp proves a status for a message we never
// sent neither errors nor touches an unrelated row.
func TestHandleEvent_UnknownMessageIDIsNoOp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	body := statusBody("wamid.SOMEONE_ELSE", whatsapp.StatusDelivered)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/integrations/whatsapp/webhook", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", whatsapp.Sign(testAppSecret, []byte(body)))

	rec := httptest.NewRecorder()
	r.NoError(env.handler.HandleEvent(rec, req))
	r.Equal(http.StatusNoContent, rec.Code)
	r.Empty(env.deliveryBody(t))
}

// TestHandleEvent_InboundReplyAccepted proves a user reply is accepted (200-ish)
// without any command handling and without disturbing delivery records.
func TestHandleEvent_InboundReplyAccepted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	body := `{"object":"whatsapp_business_account","entry":[{"id":"WABA","changes":[{"field":"messages",` +
		`"value":{"messages":[{"from":"33612345678","id":"wamid.IN","timestamp":"1700000000",` +
		`"type":"text","text":{"body":"ack"}}]}}]}]}`

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/integrations/whatsapp/webhook", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", whatsapp.Sign(testAppSecret, []byte(body)))

	rec := httptest.NewRecorder()
	r.NoError(env.handler.HandleEvent(rec, req))
	r.Equal(http.StatusNoContent, rec.Code)
	r.Empty(env.deliveryBody(t))
}

// TestHandleEvent_UnparseableBodyIsAcknowledged proves an authenticated but
// malformed batch is acknowledged rather than retried forever by Meta.
func TestHandleEvent_UnparseableBodyIsAcknowledged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	body := "not json at all"
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/integrations/whatsapp/webhook", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", whatsapp.Sign(testAppSecret, []byte(body)))

	rec := httptest.NewRecorder()
	r.NoError(env.handler.HandleEvent(rec, req))
	r.Equal(http.StatusNoContent, rec.Code)
}
