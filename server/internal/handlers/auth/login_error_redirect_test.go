package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedirectLoginError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/auth/github/login?org=nope", nil)

	r.NoError(redirectLoginError(rec, req, "ORGANIZATION_NOT_FOUND", "Organization not found"))
	r.Equal(http.StatusFound, rec.Code)
	r.Contains(rec.Header().Get("Location"), "/login?error=ORGANIZATION_NOT_FOUND")
	r.Contains(rec.Header().Get("Location"), "error_description=Organization+not+found")
}
