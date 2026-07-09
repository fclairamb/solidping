package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
)

// registrationEnabledViaHandler exercises the real GET /api/v1/auth/providers
// handler (ProvidersHandler.ListProviders) against cfg and returns the
// registrationEnabled flag it reports — the same value the dashboard login page
// reads. Sharing cfg with the auth Service lets a test assert the endpoint and
// Service.Register agree.
func registrationEnabledViaHandler(t *testing.T, cfg *config.Config) bool {
	t.Helper()
	r := require.New(t)

	h := NewProvidersHandler(cfg, nil)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/auth/providers", nil)
	rec := httptest.NewRecorder()
	r.NoError(h.ListProviders(rec, bunrouter.Request{Request: req}))

	var resp ProvidersResponse
	r.NoError(json.NewDecoder(rec.Body).Decode(&resp))

	return resp.RegistrationEnabled
}

// TestRegistrationOverlayPathEnablesRegister is the regression test for the
// frozen-snapshot bug: the auth Service is constructed with an EMPTY
// AuthConfig (no registration_email_pattern) — mirroring how NewServer copies
// cfg.Auth BEFORE InitializeSystemConfig runs — and the pattern is applied
// ONLY afterward by mutating the shared *config.Config pointer, exactly like
// the systemconfig overlay does at boot. Register must then SUCCEED, and the
// providers endpoint's registrationEnabled must AGREE with it in both states.
//
// A test that baked the pattern into the AuthConfig passed to NewService would
// not reproduce the bug, since the frozen copy would already carry the value.
func TestRegistrationOverlayPathEnablesRegister(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// setupAuthTestService constructs the Service with an AuthConfig that has
	// no RegistrationEmailPattern, and shares svc.fullCfg as the live pointer —
	// the same object the overlay would mutate at startup.
	svc, _, ctx := setupAuthTestService(t)
	sharedCfg := svc.fullCfg
	r.Empty(sharedCfg.Auth.RegistrationEmailPattern, "precondition: registration starts unset")

	newReq := func(email string) RegisterRequest {
		return RegisterRequest{Name: "New User", Email: email, Password: "supersecret123"}
	}

	// State 1 — pattern unset: the endpoint reports disabled AND Register 403s.
	r.False(registrationEnabledViaHandler(t, sharedCfg))
	_, err := svc.Register(ctx, newReq("someone@example.com"))
	r.ErrorIs(err, ErrRegistrationDisabled,
		"with no pattern, Register must report registration disabled")

	// Overlay path: apply the system parameter AFTER construction by mutating
	// the shared pointer — precisely what InitializeSystemConfig does between
	// NewServer and route setup.
	sharedCfg.Auth.RegistrationEmailPattern = ".*"

	// State 2 — pattern set via overlay: the endpoint reports enabled AND
	// Register now SUCCEEDS (this is the bug — it used to keep 403ing off the
	// stale frozen copy while the endpoint reported enabled).
	r.True(registrationEnabledViaHandler(t, sharedCfg))
	resp, err := svc.Register(ctx, newReq("newuser@example.com"))
	r.NoError(err, "registration enabled via the overlay path must let Register succeed")
	r.NotNil(resp)
}

// TestSessionMaxDurationSystemWideViaOverlay pins the second confirmed
// instance: a system-wide auth.session_max_duration applied via the overlay
// (mutating fullCfg.Auth AFTER construction, no org override present) is
// honored by resolveSessionMaxDuration. Reading the frozen s.cfg would return
// 0 (unlimited) here.
func TestSessionMaxDurationSystemWideViaOverlay(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, ctx := setupAuthTestService(t)

	// No override anywhere yet: unlimited.
	r.Equal(time.Duration(0), svc.resolveSessionMaxDuration(ctx, ""))

	// Overlay applies the system-wide cap after the Service was constructed.
	svc.fullCfg.Auth.SessionMaxDuration = 90 * time.Minute

	// Honored for an empty orgUID (no org override to consult).
	r.Equal(90*time.Minute, svc.resolveSessionMaxDuration(ctx, ""))
}
