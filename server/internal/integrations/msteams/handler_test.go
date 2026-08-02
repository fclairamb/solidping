package msteams

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// newTestHandler builds a Handler over the shared test service, wired to a
// fake Bot Framework so real token verification runs end to end.
func newTestHandler(t *testing.T, svc *Service) (*Handler, *fakeBotFramework) {
	t.Helper()

	fake := newFakeBotFramework(t)

	handler := NewHandler(svc, svc.cfg)
	handler.SetVerifier(newTestVerifier(fake))

	return handler, fake
}

// postActivity performs a real HTTP round-trip against HandleMessages.
func postActivity(t *testing.T, handler *Handler, token string, activity any) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(activity)
	require.NoError(t, err)

	return postRawActivity(t, handler, token, body)
}

func postRawActivity(t *testing.T, handler *Handler, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, MessagingEndpointPath, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	require.NoError(t, handler.HandleMessages(rec, req))

	return rec
}

// activityBody builds the JSON an inbound message activity would carry,
// including the serviceUrl the token is bound to.
func activityBody(tenantID, text string) map[string]any {
	return map[string]any{
		"type":       ActivityTypeMessage,
		"id":         "inbound-http-1",
		"serviceUrl": testServiceURL,
		"text":       text,
		"from":       map[string]any{"id": "29:user"},
		"recipient":  map[string]any{"id": "28:" + testAppID},
		"conversation": map[string]any{
			"id":               "19:channel-a",
			"conversationType": "channel",
		},
		"channelData": map[string]any{
			"tenant":  map[string]any{"id": tenantID},
			"team":    map[string]any{"id": "19:team", "name": "Ops"},
			"channel": map[string]any{"id": "19:channel-a", "name": "alerts"},
		},
	}
}

// TestHandleMessages_ReturnsServiceUnavailableWhenDisabled pins the
// msteams.enabled gate at the HTTP boundary.
func TestHandleMessages_ReturnsServiceUnavailableWhenDisabled(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, svc, _ := setupService(t)

	svc.cfg.MSTeams.Enabled = false

	handler, fake := newTestHandler(t, svc)

	rec := postActivity(t, handler, fake.sign(t, nil), activityBody(testTenantID, "hi"))
	r.Equal(http.StatusServiceUnavailable, rec.Code)
}

// TestHandleMessages_RejectsMissingToken is the primary authentication gate:
// this endpoint is genuinely public, so an unauthenticated activity must
// never be acted upon.
func TestHandleMessages_RejectsMissingToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(ctx, t, svc, "teams-http-noauth", testTenantID)

	handler, _ := newTestHandler(t, svc)

	rec := postActivity(t, handler, "", activityBody(testTenantID, "<at>SolidPing</at> help"))
	r.Equal(http.StatusUnauthorized, rec.Code)
	r.Empty(connector.recorded(), "an unauthenticated activity must not be dispatched")
}

// TestHandleMessages_RejectsForeignlySignedToken proves the signature is
// actually checked over HTTP, not just in the verifier unit tests.
func TestHandleMessages_RejectsForeignlySignedToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(ctx, t, svc, "teams-http-badsig", testTenantID)

	handler, fake := newTestHandler(t, svc)

	rec := postActivity(t, handler,
		fake.signWith(t, fake.otherKey, nil),
		activityBody(testTenantID, "<at>SolidPing</at> help"))

	r.Equal(http.StatusUnauthorized, rec.Code)
	r.Empty(connector.recorded())
}

// TestHandleMessages_RejectsServiceURLMismatch covers the replay binding at
// the HTTP layer: a valid token replayed with a different serviceUrl must be
// refused rather than steering our outbound calls elsewhere.
func TestHandleMessages_RejectsServiceURLMismatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	newConnection(ctx, t, svc, "teams-http-svcurl", testTenantID)

	handler, fake := newTestHandler(t, svc)

	body := activityBody(testTenantID, "<at>SolidPing</at> help")
	body["serviceUrl"] = "https://attacker.example.com/"

	rec := postActivity(t, handler, fake.sign(t, nil), body)
	r.Equal(http.StatusUnauthorized, rec.Code)
}

// TestHandleMessages_AcceptsVerifiedActivity is the positive control.
func TestHandleMessages_AcceptsVerifiedActivity(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(ctx, t, svc, "teams-http-ok", testTenantID)

	handler, fake := newTestHandler(t, svc)

	rec := postActivity(t, handler, fake.sign(t, nil),
		activityBody(testTenantID, "<at>SolidPing</at> help"))

	r.Equal(http.StatusOK, rec.Code)
	r.NotEmpty(connector.recorded(), "a verified activity must reach the dispatcher")
}

// TestHandleMessages_AlwaysAcksAfterDispatchFailure pins the "always 200"
// contract: Bot Framework retries non-2xx deliveries, so surfacing a dispatch
// error would make a failing command run again on every retry.
func TestHandleMessages_AlwaysAcksAfterDispatchFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	newConnection(ctx, t, svc, "teams-http-ack", testTenantID)

	handler, fake := newTestHandler(t, svc)

	// Make the outbound reply fail, so dispatch returns an error.
	connector.failNext(http.StatusInternalServerError)

	rec := postActivity(t, handler, fake.sign(t, nil),
		activityBody(testTenantID, "<at>SolidPing</at> help"))

	r.Equal(http.StatusOK, rec.Code)
}

// TestHandleMessages_RejectsMalformedBody covers the decode path.
func TestHandleMessages_RejectsMalformedBody(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, svc, _ := setupService(t)

	handler, fake := newTestHandler(t, svc)

	rec := postRawActivity(t, handler, fake.sign(t, nil), []byte("{not json"))
	r.Equal(http.StatusBadRequest, rec.Code)
}

// TestHandleMessages_CapsBodySize pins the 1 MiB read limit. The limit makes
// the truncated body fail to parse, which is the observable effect — the
// point is that an unauthenticated caller cannot make us buffer unbounded
// data before verification.
func TestHandleMessages_CapsBodySize(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, svc, _ := setupService(t)

	handler, fake := newTestHandler(t, svc)

	// A syntactically valid activity whose `text` alone exceeds the cap.
	oversized := []byte(`{"type":"message","text":"` + strings.Repeat("A", maxActivityBody+1024) + `"}`)
	r.Greater(len(oversized), maxActivityBody)

	rec := postRawActivity(t, handler, fake.sign(t, nil), oversized)
	r.Equal(http.StatusBadRequest, rec.Code)
}

// TestHandleMessages_HonorsSingleTenantPin is the end-to-end regression test
// for the self-hosted single-tenant path.
//
// A previous revision enforced the pin inside JWT verification, against a
// `tid` claim that Microsoft's Connector-to-Bot token does not contain — so
// setting msteams.tenant_id rejected EVERY legitimate activity and silently
// disabled the whole integration. This test drives a real token through the
// real HTTP handler with the pin set, and requires the matching tenant to be
// served and a foreign tenant to be refused.
func TestHandleMessages_HonorsSingleTenantPin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	svc.cfg.MSTeams.TenantID = testTenantID
	newConnection(ctx, t, svc, "teams-http-pin", testTenantID)

	handler, fake := newTestHandler(t, svc)

	// The pinned tenant is accepted and reaches the dispatcher.
	rec := postActivity(t, handler, fake.sign(t, nil),
		activityBody(testTenantID, "<at>SolidPing</at> help"))
	r.Equal(http.StatusOK, rec.Code)
	r.NotEmpty(connector.recorded(), "the pinned tenant must still be served")

	// A different tenant is turned away.
	rec = postActivity(t, handler, fake.sign(t, nil),
		activityBody("some-other-tenant", "<at>SolidPing</at> incidents list"))
	r.Equal(http.StatusOK, rec.Code)
	r.True(connector.containsText("configured for a different Microsoft 365 tenant"))
}

// TestGetStatus_ReportsInstanceState covers the (now authenticated) status
// endpoint the dashboard reads.
func TestGetStatus_ReportsInstanceState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	newConnection(ctx, t, svc, "teams-status", testTenantID)

	handler, _ := newTestHandler(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	r.NoError(handler.GetStatus(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	var resp StatusResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.True(resp.Enabled)
	r.True(resp.Configured)
	r.Contains(resp.MessagingEndpoint, MessagingEndpointPath)
}

// TestDownloadManifest_RequiresAppID stops the endpoint emitting a package
// with an empty bot id, which Teams would silently reject at upload.
func TestDownloadManifest_RequiresAppID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, svc, _ := setupService(t)

	svc.cfg.MSTeams.AppID = ""

	handler, _ := newTestHandler(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/manifest.zip", nil)
	rec := httptest.NewRecorder()
	r.NoError(handler.DownloadManifest(rec, req))
	r.Equal(http.StatusConflict, rec.Code)
}

// TestGetDestinations_ServesConnectionState exercises the destinations
// endpoint through the handler rather than the service.
func TestGetDestinations_ServesConnectionState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	conn := newConnection(ctx, t, svc, "teams-http-dest", testTenantID)

	org, err := svc.db.GetOrganization(ctx, conn.OrganizationUID)
	r.NoError(err)

	resp, err := svc.GetDestinations(ctx, org.Slug, conn.UID)
	r.NoError(err)
	r.Equal(testTenantID, resp.TenantID)
	r.True(resp.Connected)
	r.Empty(resp.Destinations)

	// And the model round-trips through the settings blob unchanged.
	settings, err := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Equal(testTenantID, settings.TenantID)
}
