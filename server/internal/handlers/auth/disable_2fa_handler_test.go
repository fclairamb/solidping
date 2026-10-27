package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// TestDisable2FAHandlerRateLimitMapsTo429 checks that the throttled
// DELETE /auth/2fa answers 429 RATE_LIMITED (spec 2026-10-27-02), not
// another INVALID_2FA_CODE the client could keep retrying.
func TestDisable2FAHandlerRateLimitMapsTo429(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, dbSvc, ctx := setupAuthTestService(t)
	svc, _, user, secret := newTwoFAThrottleFixture(t, dbSvc, ctx, "dis-http")
	handler := NewHandler(svc, &config.Config{})
	body := `{"code":"` + wrongTOTPCode(t, secret) + `"}`

	deleteTwoFA := func() *httptest.ResponseRecorder {
		reqCtx := context.WithValue(ctx, base.ContextKeyClaims, &Claims{UserUID: user.UID})
		req := httptest.NewRequestWithContext(reqCtx, http.MethodDelete, "/api/v1/auth/2fa", strings.NewReader(body))
		rec := httptest.NewRecorder()
		r.NoError(handler.Disable2FA(rec, req))

		return rec
	}

	for i := 0; i < twoFAAttemptMaxPerUser; i++ {
		r.Equal(http.StatusUnauthorized, deleteTwoFA().Code)
	}

	rec := deleteTwoFA()
	r.Equal(http.StatusTooManyRequests, rec.Code)
	r.Equal(string(base.ErrorCodeRateLimited), decodeErrorCode(t, rec))
}
