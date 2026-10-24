package slack

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandlerHTTPEntryPoints(t *testing.T) {
	t.Parallel()

	h, _, _ := solidpingSetup(t)

	jsonReq := func(body string) *http.Request {
		return httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events", strings.NewReader(body))
	}
	formReq := func(vals url.Values) *http.Request {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", strings.NewReader(vals.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		return req
	}
	badFormReq := func() *http.Request {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", strings.NewReader("%zz"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		return req
	}

	tests := []struct {
		name     string
		call     func(http.ResponseWriter, *http.Request) error
		req      *http.Request
		wantCode int
		wantBody string
	}{
		{"events invalid json", h.HandleEvents, jsonReq("{"), http.StatusBadRequest, "Invalid JSON payload"},
		{
			"events challenge", h.HandleEvents, jsonReq(`{"type":"url_verification","challenge":"abc"}`),
			http.StatusOK, "abc",
		},
		{
			"events callback error swallowed", h.HandleEvents,
			jsonReq(`{"type":"event_callback","team_id":"unknown","event":{"type":"app_home_opened","tab":"home"}}`),
			http.StatusOK, "",
		},
		{"command bad form", h.HandleCommand, badFormReq(), http.StatusBadRequest, "Invalid form data"},
		{
			"command error is ephemeral", h.HandleCommand,
			formReq(url.Values{"team_id": {"unknown"}, "command": {"/solidping"}, "text": {"checks"}}),
			http.StatusOK, "",
		},
		{"interaction bad form", h.HandleInteraction, badFormReq(), http.StatusBadRequest, "Invalid form data"},
		{
			"interaction missing payload", h.HandleInteraction, formReq(url.Values{}),
			http.StatusBadRequest, "Missing payload",
		},
		{
			"interaction invalid payload", h.HandleInteraction, formReq(url.Values{"payload": {"{"}}),
			http.StatusBadRequest, "Invalid payload JSON",
		},
		{
			"interaction unknown type", h.HandleInteraction,
			formReq(url.Values{"payload": {`{"type":"zzz"}`}}), http.StatusOK, "",
		},
		{
			"interaction handler error", h.HandleInteraction,
			formReq(url.Values{"payload": {`{"type":"view_submission","team":{"id":"unknown"}}`}}),
			http.StatusOK, "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			_ = tt.call(rec, tt.req)
			require.Equal(t, tt.wantCode, rec.Code)
			require.Contains(t, rec.Body.String(), tt.wantBody)
		})
	}
}

func TestOAuthCallbackErrorRedirects(t *testing.T) {
	t.Parallel()

	h, _, _ := solidpingSetup(t)

	tests := []struct {
		query  string
		reason string
	}{
		{"error=access_denied", "oauth_failed"},
		{"", "state_invalid"},
		{"code=abc", "state_invalid"},
		{"code=abc&state=garbage", "state_invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/cb?"+tt.query, nil)
			require.NoError(t, h.OAuthCallback(rec, req))
			require.Equal(t, http.StatusFound, rec.Code)
			require.Contains(t, rec.Header().Get("Location"), "reason="+tt.reason)
		})
	}
}
