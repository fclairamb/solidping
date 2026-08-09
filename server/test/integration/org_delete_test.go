package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestDeleteOrgIsOwnerOnly proves the deletion gate server-side rather than by
// hiding a button: an ADMIN of the org is refused with the repo's standard
// error shape, and the org survives; the OWNER of the same org succeeds.
func TestDeleteOrgIsOwnerOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	testServer := NewTestServer(t)
	dbService := testServer.Server.DBService()

	// The creator of an org is its owner (spec 2026-08-08-11) — build the org
	// through the real endpoint so the whole chain is exercised.
	passwordHash, err := passwords.Hash("testpassword123")
	r.NoError(err)

	founder := models.NewUser("founder@deletetest.example")
	founder.PasswordHash = &passwordHash
	r.NoError(dbService.CreateUser(ctx, founder))

	apiClient := testServer.NewClient()
	loginResp, err := apiClient.Login(ctx, "", founder.Email, "testpassword123")
	r.NoError(err)
	r.NotNil(loginResp.AccessToken)

	status, data := doBearerRequest(t, testServer, *loginResp.AccessToken, http.MethodPost, "/api/v1/orgs",
		map[string]any{"name": "Delete Me", "slug": "delete-me"})
	r.Equal(http.StatusCreated, status, "create org: body=%s", data)

	var created createOrgResponse
	r.NoError(json.Unmarshal(data, &created))

	ownerToken := created.AccessToken

	// The creator really is an owner in the database, not just in the token.
	founderMember, err := dbService.GetMemberByUserAndOrg(ctx, founder.UID, created.UID)
	r.NoError(err)
	r.Equal(models.MemberRoleOwner, founderMember.Role)

	// Add a second user as a plain ADMIN of the same org and log them in so
	// their token carries the admin role for this org.
	admin := models.NewUser("admin@deletetest.example")
	admin.PasswordHash = &passwordHash
	r.NoError(dbService.CreateUser(ctx, admin))

	adminMember := models.NewOrganizationMember(created.UID, admin.UID, models.MemberRoleAdmin)
	joined := time.Now()
	adminMember.JoinedAt = &joined
	r.NoError(dbService.CreateOrganizationMember(ctx, adminMember))

	adminLogin, err := apiClient.Login(ctx, created.Slug, admin.Email, "testpassword123")
	r.NoError(err)
	r.NotNil(adminLogin.AccessToken)

	adminToken := *adminLogin.AccessToken

	// Positive control: the admin token IS good enough for an admin-gated
	// route, so a 403 below is about the OWNER gate, not a broken session.
	status, body := doBearerRequest(t, testServer, adminToken, http.MethodGet,
		"/api/v1/orgs/"+created.Slug+"/checks", nil)
	r.Equal(http.StatusOK, status, "admin on an org route: body=%s", body)

	// Negative: the admin cannot delete the org.
	status, body = doBearerRequest(t, testServer, adminToken, http.MethodDelete,
		"/api/v1/orgs/"+created.Slug, map[string]any{"slug": created.Slug})
	r.Equal(http.StatusForbidden, status, "admin delete: body=%s", body)

	var errBody map[string]any
	r.NoError(json.Unmarshal(body, &errBody))
	r.Equal("FORBIDDEN", errBody["code"])

	// The org is untouched.
	stillAlive, err := dbService.GetOrganizationBySlug(ctx, created.Slug)
	r.NoError(err)
	r.Equal(created.UID, stillAlive.UID)

	// Wrong confirmation slug from the OWNER is a validation error, not a
	// delete. 422 is what base.WriteValidationError emits repo-wide; the
	// machine-readable contract is the VALIDATION_ERROR code asserted below.
	status, body = doBearerRequest(t, testServer, ownerToken, http.MethodDelete,
		"/api/v1/orgs/"+created.Slug, map[string]any{"slug": "not-the-slug"})
	r.Equal(http.StatusUnprocessableEntity, status, "owner delete with wrong slug: body=%s", body)

	r.NoError(json.Unmarshal(body, &errBody))
	r.Equal("VALIDATION_ERROR", errBody["code"])

	stillAlive, err = dbService.GetOrganizationBySlug(ctx, created.Slug)
	r.NoError(err)
	r.Equal(created.UID, stillAlive.UID)

	// Control: the owner, with the right slug, deletes it.
	status, body = doBearerRequest(t, testServer, ownerToken, http.MethodDelete,
		"/api/v1/orgs/"+created.Slug, map[string]any{"slug": created.Slug})
	r.Equal(http.StatusNoContent, status, "owner delete: body=%s", body)

	_, err = dbService.GetOrganizationBySlug(ctx, created.Slug)
	r.Error(err, "the deleted org must no longer resolve by slug")
}

// TestDeletedOrgSurfaces404Immediately walks the surfaces a deleted org must
// stop serving: the authenticated API (with the owner's own still-unexpired
// token), the public status page, its summary and its SVG badge. Each is
// asserted alive first, so a 404 afterwards can only come from the deletion.
func TestDeletedOrgSurfaces404Immediately(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	testServer := NewTestServer(t)
	dbService := testServer.Server.DBService()

	passwordHash, err := passwords.Hash("testpassword123")
	r.NoError(err)

	founder := models.NewUser("founder@surfaces.example")
	founder.PasswordHash = &passwordHash
	r.NoError(dbService.CreateUser(ctx, founder))

	apiClient := testServer.NewClient()
	loginResp, err := apiClient.Login(ctx, "", founder.Email, "testpassword123")
	r.NoError(err)

	status, data := doBearerRequest(t, testServer, *loginResp.AccessToken, http.MethodPost, "/api/v1/orgs",
		map[string]any{"name": "Surfaces Co", "slug": "surfaces-co"})
	r.Equal(http.StatusCreated, status, "create org: body=%s", data)

	var created createOrgResponse
	r.NoError(json.Unmarshal(data, &created))

	ownerToken := created.AccessToken

	// A public status page for the org.
	status, data = doBearerRequest(t, testServer, ownerToken, http.MethodPost,
		"/api/v1/orgs/"+created.Slug+"/status-pages",
		map[string]any{"name": "Public", "slug": "public", "isPublic": true})
	r.Equal(http.StatusCreated, status, "create status page: body=%s", data)

	pagePath := "/api/v1/status-pages/" + created.Slug + "/public"

	// Controls: everything answers before the delete.
	aliveChecks := []struct {
		name string
		path string
	}{
		{"api checks", "/api/v1/orgs/" + created.Slug + "/checks"},
		{"status page", pagePath},
		{"status page summary", pagePath + "/summary"},
		{"status page badge", pagePath + "/badge"},
	}

	for _, surface := range aliveChecks {
		code, respBody := doBearerRequest(t, testServer, ownerToken, http.MethodGet, surface.path, nil)
		r.Equalf(http.StatusOK, code, "%s before delete: body=%s", surface.name, respBody)
	}

	// Delete the org.
	status, body := doBearerRequest(t, testServer, ownerToken, http.MethodDelete,
		"/api/v1/orgs/"+created.Slug, map[string]any{"slug": created.Slug})
	r.Equal(http.StatusNoContent, status, "delete: body=%s", body)

	// Every surface now 404s — including the public ones, which never had auth
	// in front of them, and including the authenticated API called with the
	// owner's own token (still cryptographically valid, but the org is gone).
	for _, surface := range aliveChecks {
		code, respBody := doBearerRequest(t, testServer, ownerToken, http.MethodGet, surface.path, nil)
		r.Equalf(http.StatusNotFound, code, "%s after delete: body=%s", surface.name, respBody)
	}

	// An anonymous caller sees the same 404 on the public surfaces (no token).
	for _, path := range []string{pagePath, pagePath + "/summary", pagePath + "/badge"} {
		code, respBody := doBearerRequest(t, testServer, "", http.MethodGet, path, nil)
		r.Equalf(http.StatusNotFound, code, "anonymous %s after delete: body=%s", path, respBody)
	}

	// And the slug is free again: a new org may claim it, with none of the old
	// org's status pages carried over (no alias, no tombstone).
	status, data = doBearerRequest(t, testServer, *loginResp.AccessToken, http.MethodPost, "/api/v1/orgs",
		map[string]any{"name": "Surfaces Reborn", "slug": "surfaces-co"})
	r.Equal(http.StatusCreated, status, "recreate org on the freed slug: body=%s", data)

	var reborn createOrgResponse
	r.NoError(json.Unmarshal(data, &reborn))
	r.NotEqual(created.UID, reborn.UID)

	status, body = doBearerRequest(t, testServer, reborn.AccessToken, http.MethodGet, pagePath, nil)
	r.Equalf(http.StatusNotFound, status, "old status page under the reused slug: body=%s", body)
}
