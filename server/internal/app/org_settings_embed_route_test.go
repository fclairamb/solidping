package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOrgSettingsPatchWithInvalidEmbedOriginIsAtomic drives PATCH
// /api/v1/orgs/:org/settings over real HTTP (spec 2026-09-25-28): a valid
// field plus an invalid embed origin answers 400 VALIDATION_ERROR and leaves
// the valid field unsaved.
func TestOrgSettingsPatchWithInvalidEmbedOriginIsAtomic(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newProfileEnv(t)
	path := "/api/v1/orgs/" + env.orgSlug + "/settings"

	type settings struct {
		RegistrationEmailPattern      string   `json:"registrationEmailPattern"`
		StatusPageAllowedEmbedOrigins []string `json:"statusPageAllowedEmbedOrigins"`
	}

	read := func() settings {
		resp := env.do(http.MethodGet, path, env.adminJWT, "", nil)
		defer func() { _ = resp.Body.Close() }()

		r.Equal(http.StatusOK, resp.StatusCode)

		var got settings
		r.NoError(json.NewDecoder(resp.Body).Decode(&got))

		return got
	}

	patch := func(body string) (int, map[string]any) {
		resp := env.do(http.MethodPatch, path, env.adminJWT, "application/json", strings.NewReader(body))
		defer func() { _ = resp.Body.Close() }()

		var payload map[string]any
		r.NoError(json.NewDecoder(resp.Body).Decode(&payload))

		return resp.StatusCode, payload
	}

	status, payload := patch(`{"registrationEmailPattern":"^[^@]+@acme\\.com$",` +
		`"statusPageAllowedEmbedOrigins":["https://acme.com/status"]}`)
	r.Equal(http.StatusBadRequest, status)
	r.Equal("VALIDATION_ERROR", payload["code"])

	got := read()
	r.Empty(got.RegistrationEmailPattern, "the refused PATCH must not have saved the pattern")
	r.Empty(got.StatusPageAllowedEmbedOrigins)

	// Positive control: with a valid origin the same request goes through.
	status, _ = patch(`{"registrationEmailPattern":"^[^@]+@acme\\.com$",` +
		`"statusPageAllowedEmbedOrigins":["https://intranet.acme.com"]}`)
	r.Equal(http.StatusOK, status)

	got = read()
	r.Equal(`^[^@]+@acme\.com$`, got.RegistrationEmailPattern)
	r.Equal([]string{"https://intranet.acme.com"}, got.StatusPageAllowedEmbedOrigins)
}
