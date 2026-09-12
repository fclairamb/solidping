package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// readBody drains a response body and returns it as a string.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return string(raw)
}

// TestOrgParametersRoutesAreAdminOnlyAndNeverReturnASecret covers the API half
// of spec 2026-09-11-03 through the REAL router, because every one of these
// properties is enforced by something a handler-only test would not run: the
// admin gate is route middleware, and the secret elision is the service's
// projection.
//
// The three negatives the spec names are asserted here: a member cannot reach
// the routes at all, a secret parameter never gives its value back, and the
// reserved namespace is refused.
func TestOrgParametersRoutesAreAdminOnlyAndNeverReturnASecret(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMembersRouteEnv(t)
	base := "/api/v1/orgs/" + env.orgSlug + "/parameters"

	put := func(role models.MemberRole, key string, body any) *http.Response {
		raw, err := json.Marshal(body)
		r.NoError(err)

		return env.do(http.MethodPut, base+"/"+key, env.jwts[role], "application/json", bytes.NewReader(raw))
	}

	// --- A non-admin member cannot even list. Not "sees an empty list": 403. ---
	for _, role := range []models.MemberRole{models.MemberRoleViewer, models.MemberRoleUser} {
		resp := env.do(http.MethodGet, base, env.jwts[role], "", nil)
		r.Equal(http.StatusForbidden, resp.StatusCode, "GET must be admin-only, role %s", role)
		_ = resp.Body.Close()

		resp = put(role, "sso-password", map[string]any{"value": "hunter2", "secret": true})
		r.Equal(http.StatusForbidden, resp.StatusCode, "PUT must be admin-only, role %s", role)
		_ = resp.Body.Close()

		resp = env.do(http.MethodDelete, base+"/sso-password", env.jwts[role], "", nil)
		r.Equal(http.StatusForbidden, resp.StatusCode, "DELETE must be admin-only, role %s", role)
		_ = resp.Body.Close()
	}

	// --- An admin creates a SECRET parameter. ---
	resp := put(models.MemberRoleAdmin, "sso-password", map[string]any{"value": "hunter2", "secret": true})
	body := readBody(t, resp)
	_ = resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode, body)
	r.NotContains(body, "hunter2", "the PUT response must not echo a secret value back")

	// …and a NON-secret one, which does come back whole: the elision has to be
	// the secret flag doing work, not the endpoint returning nothing ever.
	resp = put(models.MemberRoleAdmin, "region-label", map[string]any{"value": "paris", "secret": false})
	body = readBody(t, resp)
	_ = resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode, body)
	r.Contains(body, "paris", "a non-secret value is readable — otherwise this test proves nothing")

	// --- GET of the secret key: key, flag and timestamp, no value. ---
	resp = env.do(http.MethodGet, base+"/sso-password", env.jwts[models.MemberRoleAdmin], "", nil)
	body = readBody(t, resp)
	_ = resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Contains(body, `"secret":true`)
	r.NotContains(body, "hunter2", "GET must never return a secret parameter's value")

	// --- LIST: both keys present, the secret one still without its value. ---
	resp = env.do(http.MethodGet, base, env.jwts[models.MemberRoleAdmin], "", nil)
	body = readBody(t, resp)
	_ = resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Contains(body, `"data":[`, "list responses are wrapped in data")
	r.Contains(body, "sso-password")
	r.Contains(body, "region-label")
	r.NotContains(body, "hunter2", "the list must never return a secret parameter's value")

	// --- The reserved SolidPing namespace is refused. ---
	for _, key := range []string{"sp.anything", "sp.deeply.nested"} {
		resp = put(models.MemberRoleAdmin, key, map[string]any{"value": "x", "secret": true})
		body = readBody(t, resp)
		_ = resp.Body.Close()
		r.Equalf(http.StatusBadRequest, resp.StatusCode, "reserved key %q must be refused: %s", key, body)
		r.Contains(body, `"code"`, "the refusal uses the standard error shape")
	}

	// --- A key NAMED like platform material is accepted, and lands nowhere
	// near it. This is the property that replaced a denylist: the protection is
	// the storage namespace, so there is no name to forbid and no list to keep
	// in sync (the previous denylist was missing four instance credentials on
	// the day it was written).
	//
	// Asserted against the database, not the API: the API cannot show where a
	// row lives, and "somewhere else" is the entire claim. ---
	ctx := context.Background()
	platformValue := "the-real-wrapped-dek"
	r.NoError(env.server.dbService.SetOrgParameter(ctx, env.orgUID, "encryption.dek", platformValue, true))

	resp = put(models.MemberRoleAdmin, "encryption.dek", map[string]any{"value": "org-supplied", "secret": true})
	body = readBody(t, resp)
	_ = resp.Body.Close()
	r.Equalf(http.StatusOK, resp.StatusCode, "a look-alike key is ordinary org data: %s", body)

	platformRow, err := env.server.dbService.GetOrgParameter(ctx, env.orgUID, "encryption.dek")
	r.NoError(err)
	r.NotNil(platformRow, "the platform row must still exist")
	r.Equal(platformValue, platformRow.Value[models.ParameterValueKey],
		"an org admin must not be able to overwrite the key its own DEK lives under")

	orgRow, err := env.server.dbService.GetOrgParameter(
		ctx, env.orgUID, paramkeys.StorageKey("encryption.dek"))
	r.NoError(err)
	r.NotNil(orgRow, "the org's own value lives in the org namespace")
	r.Equal("org-supplied", orgRow.Value[models.ParameterValueKey])

	// A malformed key is a 400 too — uppercase is not in the pattern.
	resp = put(models.MemberRoleAdmin, "NotLowercase", map[string]any{"value": "x"})
	_ = resp.Body.Close()
	r.Equal(http.StatusBadRequest, resp.StatusCode)

	// --- Delete works for an admin, and deleting nothing is a 404, not a
	// cheerful 204: a parameter's disappearance breaks every check that
	// references it, so "I deleted something that was not there" must be said. ---
	resp = env.do(http.MethodDelete, base+"/region-label", env.jwts[models.MemberRoleAdmin], "", nil)
	_ = resp.Body.Close()
	r.Equal(http.StatusNoContent, resp.StatusCode)

	resp = env.do(http.MethodDelete, base+"/region-label", env.jwts[models.MemberRoleAdmin], "", nil)
	_ = resp.Body.Close()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}
