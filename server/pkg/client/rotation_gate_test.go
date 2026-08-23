package client_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/client"
)

// newGateServer answers every request with a 403 carrying `code`.
func newGateServer(t *testing.T, code string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"title":"Denied","code":"` + code + `","detail":"d"}`))
	}))
	t.Cleanup(srv.Close)

	return srv
}

// TestForcedRotationSurfacesTypedError is the CLI half of spec 2026-08-23-04:
// the operator must be told to rotate, not handed "unexpected response status:
// 403", which reads as a missing permission and sends them hunting for a role
// to grant.
func TestForcedRotationSurfacesTypedError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newGateServer(t, "PASSWORD_CHANGE_REQUIRED")

	api, err := client.New(client.Config{BaseURL: srv.URL})
	r.NoError(err)

	_, err = api.ListCheckDependencies(t.Context(), "acme", "web")
	r.ErrorIs(err, client.ErrPasswordChangeRequired)
	r.Contains(err.Error(), "change-password", "the message must name the way out")
}

// TestOrdinaryForbiddenIsUnaffected is the positive control. The gate reads the
// body of every 403; if it failed to restore that body, or matched too
// loosely, a genuine permission error would either break decoding or be
// mislabelled as a password problem.
func TestOrdinaryForbiddenIsUnaffected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newGateServer(t, "FORBIDDEN")

	api, err := client.New(client.Config{BaseURL: srv.URL})
	r.NoError(err)

	_, err = api.ListCheckDependencies(t.Context(), "acme", "web")
	r.Error(err)
	r.NotErrorIs(err, client.ErrPasswordChangeRequired)
	r.Contains(err.Error(), "Denied", "the server's own message must still reach the caller")
}
