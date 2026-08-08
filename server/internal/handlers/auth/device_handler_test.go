package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// postDevice drives one of the two public device endpoints and returns the
// recorder plus the decoded JSON body.
func postDevice(
	ctx context.Context, t *testing.T,
	fn func(http.ResponseWriter, *http.Request) error, path, body string,
) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	require.NoError(t, fn(rec, req))

	decoded := map[string]any{}
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	}

	return rec, decoded
}

// startDeviceRequest opens a request through the HTTP surface and returns the
// wire-format device_code / user_code.
func startDeviceRequest(ctx context.Context, t *testing.T, handler *Handler, clientName string) (string, string) {
	t.Helper()

	rec, body := postDevice(ctx, t, handler.StartDeviceAuthorization,
		"/api/v1/auth/device", `{"clientName":"`+clientName+`"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	deviceCode, ok := body["device_code"].(string)
	require.True(t, ok, "response must carry the RFC 8628 device_code field")

	userCode, ok := body["user_code"].(string)
	require.True(t, ok, "response must carry the RFC 8628 user_code field")

	return deviceCode, userCode
}

func tokenRequestBody(deviceCode string) string {
	return `{"grant_type":"` + DeviceGrantType + `","device_code":"` + deviceCode + `"}`
}

// TestDeviceAuthorizationEndpointWireFormat pins the RFC 8628 field names on
// the public endpoints — the interoperable contract, deliberately snake_case
// where the rest of the API is camelCase.
func TestDeviceAuthorizationEndpointWireFormat(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, ctx := setupAuthTestServiceWithConfig(t, "https://ping.example.com")
	handler := NewHandler(svc, &config.Config{})

	rec, body := postDevice(ctx, t, handler.StartDeviceAuthorization,
		"/api/v1/auth/device", `{"clientName":"sp CLI on laptop"}`)
	r.Equal(http.StatusOK, rec.Code)

	for _, field := range []string{
		"device_code", "user_code", "verification_uri", "verification_uri_complete", "expires_in", "interval",
	} {
		r.Contains(body, field, "RFC 8628 field %q must be spelled exactly as the RFC does", field)
	}

	r.Equal("https://ping.example.com/dash0/device", body["verification_uri"])
}

// TestDeviceTokenEndpointErrorCodes walks the RFC 8628 §3.5 error vocabulary
// through the HTTP surface, including the exactly-once guarantee.
func TestDeviceTokenEndpointErrorCodes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://ping.example.com")
	handler := NewHandler(svc, &config.Config{})

	org, _, claims := deviceTestUser(ctx, t, dbSvc, "device-http", "device-http@example.com")

	// Unsupported grant type.
	rec, body := postDevice(ctx, t, handler.PollDeviceToken,
		"/api/v1/auth/device/token", `{"grant_type":"password","device_code":"x"}`)
	r.Equal(http.StatusBadRequest, rec.Code)
	r.Equal("unsupported_grant_type", body["error"])

	// Malformed body.
	rec, body = postDevice(ctx, t, handler.PollDeviceToken, "/api/v1/auth/device/token", `not json`)
	r.Equal(http.StatusBadRequest, rec.Code)
	r.Equal("invalid_request", body["error"])

	// Unknown device code.
	rec, body = postDevice(ctx, t, handler.PollDeviceToken,
		"/api/v1/auth/device/token", tokenRequestBody("nope"))
	r.Equal(http.StatusBadRequest, rec.Code)
	r.Equal("expired_token", body["error"])

	deviceCode, userCode := startDeviceRequest(ctx, t, handler, "sp CLI")

	// Pending.
	rec, body = postDevice(ctx, t, handler.PollDeviceToken,
		"/api/v1/auth/device/token", tokenRequestBody(deviceCode))
	r.Equal(http.StatusBadRequest, rec.Code)
	r.Equal("authorization_pending", body["error"])

	// Immediately again → slow_down.
	_, body = postDevice(ctx, t, handler.PollDeviceToken,
		"/api/v1/auth/device/token", tokenRequestBody(deviceCode))
	r.Equal("slow_down", body["error"])

	// Approve, then collect the token exactly once.
	r.NoError(svc.RespondToDeviceConsent(ctx, claims, userCode, org.Slug, true))
	clearDevicePollStamp(ctx, t, dbSvc, deviceCode)

	rec, body = postDevice(ctx, t, handler.PollDeviceToken,
		"/api/v1/auth/device/token", tokenRequestBody(deviceCode))
	r.Equal(http.StatusOK, rec.Code)
	r.Equal("Bearer", body["token_type"])

	accessToken, ok := body["access_token"].(string)
	r.True(ok)
	r.True(strings.HasPrefix(accessToken, "pat_"), "the device flow hands back a PAT")

	// And never again.
	rec, body = postDevice(ctx, t, handler.PollDeviceToken,
		"/api/v1/auth/device/token", tokenRequestBody(deviceCode))
	r.Equal(http.StatusBadRequest, rec.Code)
	r.Equal("expired_token", body["error"], "a poll after delivery must not re-deliver the PAT")
}

// TestDeviceConsentEndpointsRequireAuth guards the consent surface: without
// claims it 401s and leaks nothing about whether the user code exists.
func TestDeviceConsentEndpointsRequireAuth(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, ctx := setupAuthTestServiceWithConfig(t, "https://ping.example.com")
	handler := NewHandler(svc, &config.Config{})

	_, userCode := startDeviceRequest(ctx, t, handler, "sp CLI")

	req := httptest.NewRequestWithContext(ctx, http.MethodGet,
		"/api/v1/auth/device/consent?userCode="+strings.ReplaceAll(userCode, "-", ""), http.NoBody)
	rec := httptest.NewRecorder()
	r.NoError(handler.GetDeviceConsent(rec, req))
	r.Equal(http.StatusUnauthorized, rec.Code)

	req = httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/device/consent",
		strings.NewReader(`{"userCode":"`+userCode+`","approve":true}`))
	rec = httptest.NewRecorder()
	r.NoError(handler.RespondToDeviceConsent(rec, req))
	r.Equal(http.StatusUnauthorized, rec.Code)
}

// TestDeviceConsentEndpoints covers the authenticated consent lookup and
// decision over HTTP, including the case-insensitive user-code lookup and the
// conflict on a second decision.
func TestDeviceConsentEndpoints(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://ping.example.com")
	handler := NewHandler(svc, &config.Config{})

	org, _, claims := deviceTestUser(ctx, t, dbSvc, "device-consent", "device-consent@example.com")
	authCtx := context.WithValue(ctx, base.ContextKeyClaims, claims)

	deviceCode, userCode := startDeviceRequest(ctx, t, handler, "sp CLI on laptop")

	// The lookup is case/separator insensitive.
	for _, spelling := range []string{userCode, strings.ToLower(userCode), strings.ReplaceAll(userCode, "-", "")} {
		req := httptest.NewRequestWithContext(authCtx, http.MethodGet,
			"/api/v1/auth/device/consent?userCode="+spelling, http.NoBody)
		rec := httptest.NewRecorder()
		r.NoError(handler.GetDeviceConsent(rec, req))
		r.Equal(http.StatusOK, rec.Code, "spelling %q must resolve", spelling)

		info := DeviceConsentInfo{}
		r.NoError(json.Unmarshal(rec.Body.Bytes(), &info))
		r.Equal("sp CLI on laptop", info.ClientName)
		r.Equal(userCode, info.UserCode)
		r.Equal("pending", info.Status)
	}

	// An unknown code is a 404, not a 500.
	req := httptest.NewRequestWithContext(authCtx, http.MethodGet,
		"/api/v1/auth/device/consent?userCode=ZZZZ-9999", http.NoBody)
	rec := httptest.NewRecorder()
	r.NoError(handler.GetDeviceConsent(rec, req))
	r.Equal(http.StatusNotFound, rec.Code)

	// Approve.
	rec = postDeviceAuthed(authCtx, t, handler, `{"userCode":"`+userCode+`","org":"`+org.Slug+`","approve":true}`)
	r.Equal(http.StatusOK, rec.Code)

	// Deciding again conflicts.
	rec = postDeviceAuthed(authCtx, t, handler, `{"userCode":"`+userCode+`","org":"`+org.Slug+`","approve":false}`)
	r.Equal(http.StatusConflict, rec.Code)

	// A missing user code is a validation error (the house 422 shape).
	rec = postDeviceAuthed(authCtx, t, handler, `{"approve":true}`)
	r.Equal(http.StatusUnprocessableEntity, rec.Code)

	// The approval is real: the token endpoint hands back the PAT.
	clearDevicePollStamp(ctx, t, dbSvc, deviceCode)
	_, body := postDevice(ctx, t, handler.PollDeviceToken,
		"/api/v1/auth/device/token", tokenRequestBody(deviceCode))
	r.NotEmpty(body["access_token"])
}

// postDeviceAuthed posts a consent decision with claims already on the context.
func postDeviceAuthed(
	ctx context.Context, t *testing.T, handler *Handler, body string,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/device/consent", strings.NewReader(body))
	rec := httptest.NewRecorder()
	require.NoError(t, handler.RespondToDeviceConsent(rec, req))

	return rec
}
