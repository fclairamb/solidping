package system

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// This file covers spec 2026-09-12-03: PUT/GET/DELETE
// /api/v1/system/parameters/:key handed the URL key straight to the database.
// Postgres's CHECK constraint on parameters.key turned a bad key into a raw
// 500; SQLite has no such CHECK at all, so the very same request silently
// succeeded there instead — an engine divergence. The fix validates the key
// shape in the application layer (system.Service, via
// paramkeys.KeyPattern — deliberately NOT paramkeys.Validate, which also
// refuses the "sp." prefix reserved FOR the platform) so both engines answer
// identically.
//
// These tests run against the in-memory SQLite harness (newOpsEnv), which is
// exactly the engine on which this bug was invisible before the fix: on
// `main`, PUT with a bad key answers 200 here, not the 500 Postgres would
// have produced. Asserting the response status/code/field below is therefore
// a real regression test, not merely a "no 500" check.

// newSystemParamRequest builds an httptest.Request with the chi route param
// set the way the real router would for /api/v1/system/parameters/{key}. The
// request line always targets a fixed, valid-looking path — some of the keys
// under test (a space, a slash, a quote) are not valid raw request-URI bytes,
// and httpx.Param only ever consults the injected chi route context below,
// never the literal path — so the fixed path is just a placeholder.
func newSystemParamRequest(method, key, body string) (*http.Request, *httptest.ResponseRecorder) {
	const target = "/api/v1/system/parameters/placeholder"

	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("key", key)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	return req, httptest.NewRecorder()
}

// TestSystemParameterKeyValidation_RejectsMalformedKeys is the regression
// test: it must fail on `main`, where SQLite (no CHECK constraint on
// parameters.key) answers 200 for every one of these keys instead of 400.
func TestSystemParameterKeyValidation_RejectsMalformedKeys(t *testing.T) {
	t.Parallel()

	// The shapes paramkeys.KeyPattern refuses: an uppercase letter, a space, a
	// slash, a colon, a quote, an empty string, and a leading digit — the
	// exact list spec 2026-09-12-03 calls out.
	malformedKeys := []struct {
		name string
		key  string
	}{
		{"uppercase", "Uppercase"},
		{"space", "has space"},
		{"slash", "has/slash"},
		{"colon", "has:colon"},
		{"quote", `has"quote`},
		{"empty", ""},
		{"leading digit", "1leading"},
	}

	for _, tc := range malformedKeys {
		t.Run("PUT/"+tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := newOpsEnv(t)
			h := NewHandler(env.svc, nil)

			req, rec := newSystemParamRequest(http.MethodPut, tc.key, `{"value":"x"}`)
			r.NoError(h.SetParameter(rec, req))

			assertKeyValidationError(r, rec)
		})

		t.Run("GET/"+tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := newOpsEnv(t)
			h := NewHandler(env.svc, nil)

			req, rec := newSystemParamRequest(http.MethodGet, tc.key, "")
			r.NoError(h.GetParameter(rec, req))

			assertKeyValidationError(r, rec)
		})

		t.Run("DELETE/"+tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := newOpsEnv(t)
			h := NewHandler(env.svc, nil)

			req, rec := newSystemParamRequest(http.MethodDelete, tc.key, "")
			r.NoError(h.DeleteParameter(rec, req))

			assertKeyValidationError(r, rec)
		})
	}
}

// assertKeyValidationError checks the shape spec 2026-09-12-03 requires: 400,
// code VALIDATION_ERROR, field "key". The status is 400, not the 422 that
// h.WriteValidationError hardcodes (base.go:227-237) — the handler writes the
// base.ValidationError body directly at http.StatusBadRequest instead of
// through that helper. 400 is correct because the sibling org-managed route
// answers this exact error (a malformed parameter key) with 400: every
// validation branch in orgparams/handler.go's handleError, including
// errors.Is(err, paramkeys.ErrInvalidKey) at handler.go:83-86, is
// h.WriteError(..., http.StatusBadRequest, base.ErrorCodeValidationError,
// ...). The same malformed key must not get 400 from one parameters route and
// 422 from the other.
func assertKeyValidationError(r *require.Assertions, rec *httptest.ResponseRecorder) {
	r.Equal(http.StatusBadRequest, rec.Code)

	var body base.ValidationError

	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal("VALIDATION_ERROR", body.Code)
	r.Len(body.Fields, 1)
	r.Equal(keyField, body.Fields[0].Name)
}

// TestSystemParameterKeyValidation_AcceptsValidKeys is the positive control:
// a legitimate platform key must still round-trip end to end through
// SetParameter, GetParameter and DeleteParameter. Without this, a pattern
// tightened to fix the bug could just as easily start refusing real keys.
func TestSystemParameterKeyValidation_AcceptsValidKeys(t *testing.T) {
	t.Parallel()

	validKeys := []string{
		"encryption.dek",                 // flat namespace, dotted
		regions.ParamRegions,             // flat key, no dots
		models.ParamKeyTracerouteEnabled, // three-segment dotted key
	}

	for _, key := range validKeys {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := newOpsEnv(t)
			h := NewHandler(env.svc, nil)

			putReq, putRec := newSystemParamRequest(http.MethodPut, key, `{"value":"ok"}`)
			r.NoError(h.SetParameter(putRec, putReq))
			r.Equal(http.StatusOK, putRec.Code)

			getReq, getRec := newSystemParamRequest(http.MethodGet, key, "")
			r.NoError(h.GetParameter(getRec, getReq))
			r.Equal(http.StatusOK, getRec.Code)

			delReq, delRec := newSystemParamRequest(http.MethodDelete, key, "")
			r.NoError(h.DeleteParameter(delRec, delReq))
			r.Equal(http.StatusNoContent, delRec.Code)
		})
	}
}

// TestSystemParameterKeyValidation_AdHocKeysMatchPattern is half of the
// "registry proof" spec 2026-09-12-03 asks for: the ad-hoc platform keys
// named in its §1 (written as string literals rather than through a
// systemconfig.ParameterKey constant) must satisfy paramkeys.KeyPattern. The
// other half — the actual systemconfig.ParameterKey registry — is covered by
// TestGetKnownParameters_KeysMatchParamKeyPattern in
// internal/systemconfig/systemconfig_test.go, which iterates
// getKnownParameters() directly (unexported, so it cannot be reached from
// this package).
func TestSystemParameterKeyValidation_AdHocKeysMatchPattern(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	adHocKeys := []string{
		"encryption.dek", // internal/crypto/credentials: dekParameterKey (unexported)
		regions.ParamRegions,
		regions.ParamCustomRegions,
		regions.ParamDefaultRegions,
		"telegram.webhook_secret", // internal/app: TelegramWebhookSecretParam
		jobtypes.ParamDemoEnabled,
		opsnotify.ParamOperatorNotifications,
		watchdog.ParamPlatformWatchdog,
		models.ParamKeyTracerouteEnabled,
		// internal/handlers/incidentpublications: notifyCapParameterKey (unexported)
		"status_page.publication_notify_cap",
		// internal/handlers/auth: registrationEmailPatternKey (unexported)
		"registration.email_pattern",
		// internal/handlers/auth: registrationSlackAutoJoinKey (unexported)
		"registration.slack_workspace_auto_join",
	}

	pattern := paramkeys.KeyPattern
	for _, key := range adHocKeys {
		r.Truef(pattern.MatchString(key), "ad-hoc platform key %q must match %s", key, pattern.String())
	}
}
