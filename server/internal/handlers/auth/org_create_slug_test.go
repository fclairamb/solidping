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
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// TestCreateOrgDerivesSlugFromName is the proof for the "a newcomer should not
// have to invent a URL identifier" half of spec 2026-09-05-01: POST /orgs with
// no slug must succeed and derive one from the name, while every existing
// guarantee about an EXPLICIT slug (strict validation, 409 on collision) still
// holds. The negative cases are positive controls: a change that simply made
// CreateOrg lenient everywhere would fail them.
func TestCreateOrgDerivesSlugFromName(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestService(t)

	user := models.NewUser("founder@acme.example")
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	// 1. Empty slug -> derived from the name.
	first, err := svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "Alice's organization"}, Context{})
	require.NoError(t, err)
	require.Equal(t, "alices-organization", first.Slug,
		"an omitted slug must be derived from the name by orgslug.Slugify")

	// The org really exists under that slug, and the caller owns it.
	stored, err := dbSvc.GetOrganizationBySlug(ctx, first.Slug)
	require.NoError(t, err)
	require.Equal(t, first.UID, stored.UID)

	member, err := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, first.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleOwner, member.Role)

	// 2. A second create with the SAME name must not 409 — the generator
	//    appends a numeric suffix. This is the whole point of routing through
	//    GenerateUnique rather than Slugify: two people named Alice both get an
	//    org, neither meets an error they cannot act on.
	second, err := svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "Alice's organization"}, Context{})
	require.NoError(t, err)
	require.NotEqual(t, first.Slug, second.Slug)
	require.Equal(t, "alices-organization2", second.Slug)

	// 3. A name that normalizes to nothing usable still yields a valid slug
	//    rather than an error or an empty slug.
	punct, err := svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "!!"}, Context{})
	require.NoError(t, err)
	require.NotEmpty(t, punct.Slug)
}

// TestCreateOrgExplicitSlugStillStrict is the positive control for the change
// above: making the slug optional must not have made a supplied slug optional
// to be *valid*.
func TestCreateOrgExplicitSlugStillStrict(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestService(t)

	user := models.NewUser("strict@acme.example")
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	// An explicit slug is taken literally, never normalized behind the
	// caller's back.
	ok, err := svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "Acme Co", Slug: "acme-co"}, Context{})
	require.NoError(t, err)
	require.Equal(t, "acme-co", ok.Slug)

	// Invalid explicit slug -> ErrInvalidOrgSlug (422 at the handler).
	_, err = svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "Acme Co", Slug: "A B"}, Context{})
	require.ErrorIs(t, err, ErrInvalidOrgSlug)

	// Too short is still invalid.
	_, err = svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "Acme Co", Slug: "ab"}, Context{})
	require.ErrorIs(t, err, ErrInvalidOrgSlug)

	// Explicit slug already taken -> ErrOrgSlugTaken (409 at the handler).
	_, err = svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "Other Co", Slug: "acme-co"}, Context{})
	require.ErrorIs(t, err, ErrOrgSlugTaken)
}

// TestCreateOrgHandlerSlugOptional drives the change through the real HTTP
// handler, which is where the "Slug is required" 422 used to live: a body with
// only a name must now answer 201, while an empty name still answers 422.
func TestCreateOrgHandlerSlugOptional(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	handler := NewHandler(svc, &config.Config{})

	user := models.NewUser("handler-slug@acme.example")
	r.NoError(dbSvc.CreateUser(ctx, user))

	authed := context.WithValue(ctx, base.ContextKeyClaims, &Claims{UserUID: user.UID})

	// Name only -> 201, with a derived slug in the response.
	req := httptest.NewRequestWithContext(authed, http.MethodPost, "/api/v1/orgs",
		strings.NewReader(`{"name":"Bright Falcon"}`))
	rec := httptest.NewRecorder()
	r.NoError(handler.CreateOrg(rec, req))
	r.Equal(http.StatusCreated, rec.Code)

	var created OrgResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))
	r.Equal("bright-falcon", created.Slug)
	r.NotEmpty(created.AccessToken)

	// An explicitly empty name is still a validation error — dropping the slug
	// requirement must not have dropped the name one.
	req = httptest.NewRequestWithContext(authed, http.MethodPost, "/api/v1/orgs",
		strings.NewReader(`{"name":""}`))
	rec = httptest.NewRecorder()
	r.NoError(handler.CreateOrg(rec, req))
	r.Equal(http.StatusUnprocessableEntity, rec.Code)

	// An explicitly invalid slug is still 422.
	req = httptest.NewRequestWithContext(authed, http.MethodPost, "/api/v1/orgs",
		strings.NewReader(`{"name":"Bright Falcon","slug":"NO"}`))
	rec = httptest.NewRecorder()
	r.NoError(handler.CreateOrg(rec, req))
	r.Equal(http.StatusUnprocessableEntity, rec.Code)

	// An explicitly taken slug is still 409.
	req = httptest.NewRequestWithContext(authed, http.MethodPost, "/api/v1/orgs",
		strings.NewReader(`{"name":"Other","slug":"bright-falcon"}`))
	rec = httptest.NewRecorder()
	r.NoError(handler.CreateOrg(rec, req))
	r.Equal(http.StatusConflict, rec.Code)
}
