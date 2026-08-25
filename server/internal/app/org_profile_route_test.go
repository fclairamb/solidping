package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/orglogo"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

const orgProfileTestPassword = "profile-test-pass"

// profileEnv is a fully wired server with one org holding an owner and an
// admin, both with password logins, so the owner-only gate can be probed from
// both sides over real HTTP.
type profileEnv struct {
	t         *testing.T
	ts        *httptest.Server
	client    *http.Client
	orgSlug   string
	orgUID    string
	dbService db.Service
	ownerJWT  string
	adminJWT  string
}

func newProfileEnv(t *testing.T) *profileEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "org-profile-route-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour
	// Keep uploaded blobs inside the test's temp dir instead of ./data/files.
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	org := models.NewOrganization("profile-org", "Profile Org")
	r.NoError(server.dbService.CreateOrganization(ctx, org))

	env := &profileEnv{
		t: t, ts: ts, orgSlug: org.Slug, orgUID: org.UID, dbService: server.dbService,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}

	hash, err := passwords.Hash(orgProfileTestPassword)
	r.NoError(err)
	now := time.Now()

	for email, role := range map[string]models.MemberRole{
		"owner@profile.example": models.MemberRoleOwner,
		"admin@profile.example": models.MemberRoleAdmin,
	} {
		user := models.NewUser(email)
		user.PasswordHash = &hash
		user.EmailVerifiedAt = &now
		r.NoError(server.dbService.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, role)
		member.JoinedAt = &now
		r.NoError(server.dbService.CreateOrganizationMember(ctx, member))
	}

	env.ownerJWT = env.login("owner@profile.example")
	env.adminJWT = env.login("admin@profile.example")

	return env
}

func (e *profileEnv) login(email string) string {
	e.t.Helper()
	r := require.New(e.t)

	body, err := json.Marshal(map[string]string{
		"org": e.orgSlug, "email": email, "password": orgProfileTestPassword,
	})
	r.NoError(err)

	resp := e.do(http.MethodPost, "/api/v1/auth/login", "", "application/json", bytes.NewReader(body))
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)

	var payload struct {
		AccessToken string `json:"accessToken"`
	}

	r.NoError(json.NewDecoder(resp.Body).Decode(&payload))
	r.NotEmpty(payload.AccessToken)

	return payload.AccessToken
}

func (e *profileEnv) do(method, path, token, contentType string, body io.Reader) *http.Response {
	e.t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, e.ts.URL+path, body)
	require.NoError(e.t, err)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := e.client.Do(req)
	require.NoError(e.t, err)

	return resp
}

func (e *profileEnv) patchProfile(token, body string) *http.Response {
	return e.do(http.MethodPatch, "/api/v1/orgs/"+e.orgSlug, token, "application/json", strings.NewReader(body))
}

// uploadLogo posts a multipart body with the given part content type.
func (e *profileEnv) uploadLogo(token, filename, contentType string, content []byte) *http.Response {
	e.t.Helper()
	r := require.New(e.t)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="logo"; filename="`+filename+`"`)
	header.Set("Content-Type", contentType)

	part, err := writer.CreatePart(header)
	r.NoError(err)
	_, err = part.Write(content)
	r.NoError(err)
	r.NoError(writer.Close())

	return e.do(http.MethodPost, "/api/v1/orgs/"+e.orgSlug+"/logo", token, writer.FormDataContentType(), &buf)
}

// TestOrgProfileIsOwnerOnly pins the authorization gate on every profile
// mutation: an org ADMIN — who passes every other admin gate in the product —
// is refused with 403, while the owner succeeds. The admin half is the test;
// the owner half is the positive control that proves the route works at all.
func TestOrgProfileIsOwnerOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newProfileEnv(t)

	adminResp := env.patchProfile(env.adminJWT, `{"name":"Admin Was Here"}`)
	defer func() { _ = adminResp.Body.Close() }()
	r.Equal(http.StatusForbidden, adminResp.StatusCode, "an admin must not be able to edit the org profile")

	adminLogo := env.uploadLogo(env.adminJWT, "logo.png", "image/png", []byte("\x89PNG\r\n\x1a\n"))
	defer func() { _ = adminLogo.Body.Close() }()
	r.Equal(http.StatusForbidden, adminLogo.StatusCode, "an admin must not be able to upload a logo")

	ownerResp := env.patchProfile(env.ownerJWT, `{"name":"Owner Was Here"}`)
	defer func() { _ = ownerResp.Body.Close() }()
	r.Equal(http.StatusOK, ownerResp.StatusCode)

	var payload struct {
		Name string `json:"name"`
	}

	r.NoError(json.NewDecoder(ownerResp.Body).Decode(&payload))
	r.Equal("Owner Was Here", payload.Name)
}

// TestOrgProfileSlugConflictAndValidation pins the HTTP error mapping for the
// two slug refusals the dashboard renders per field.
func TestOrgProfileSlugConflictAndValidation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newProfileEnv(t)

	invalid := env.patchProfile(env.ownerJWT, `{"slug":"Nope_Not-Valid"}`)
	defer func() { _ = invalid.Body.Close() }()
	r.Equal(http.StatusUnprocessableEntity, invalid.StatusCode)

	badLogo := env.patchProfile(env.ownerJWT, `{"logoUrl":"javascript:alert(1)"}`)
	defer func() { _ = badLogo.Body.Close() }()
	r.Equal(http.StatusUnprocessableEntity, badLogo.StatusCode)

	ok := env.patchProfile(env.ownerJWT, `{"slug":"profile-renamed"}`)
	defer func() { _ = ok.Body.Close() }()
	r.Equal(http.StatusOK, ok.StatusCode)

	var payload struct {
		Slug         string `json:"slug"`
		PreviousSlug string `json:"previousSlug"`
		AccessToken  string `json:"accessToken"`
	}

	r.NoError(json.NewDecoder(ok.Body).Decode(&payload))
	r.Equal("profile-renamed", payload.Slug)
	r.Equal("profile-org", payload.PreviousSlug)
	r.NotEmpty(payload.AccessToken, "a rename must hand back a session scoped to the new slug")
}

// TestOrgLogoUploadValidationAndServing covers the logo end to end: the type
// and size allowlists, and — the binding security requirement of the spec — the
// headers the public route serves an uploaded SVG with.
func TestOrgLogoUploadValidationAndServing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newProfileEnv(t)

	// Rejected: a content type outside the image allowlist. An HTML "logo"
	// served from our own origin would be stored XSS.
	html := env.uploadLogo(env.ownerJWT, "logo.html", "text/html", []byte("<script>alert(1)</script>"))
	defer func() { _ = html.Body.Close() }()
	r.Equal(http.StatusUnprocessableEntity, html.StatusCode)

	// Rejected: over the 1 MB cap.
	oversized := env.uploadLogo(env.ownerJWT, "big.png", "image/png",
		bytes.Repeat([]byte("A"), int(orglogo.MaxLogoSize)+1024))
	defer func() { _ = oversized.Body.Close() }()
	r.Equal(http.StatusRequestEntityTooLarge, oversized.StatusCode)

	// Accepted: a PNG. Positive control for both refusals above.
	png := env.uploadLogo(env.ownerJWT, "logo.png", "image/png", []byte("\x89PNG\r\n\x1a\nfake"))
	defer func() { _ = png.Body.Close() }()
	r.Equal(http.StatusOK, png.StatusCode)

	var uploaded struct {
		LogoURL *string `json:"logoUrl"`
	}

	r.NoError(json.NewDecoder(png.Body).Decode(&uploaded))
	r.NotNil(uploaded.LogoURL)
	r.True(strings.HasPrefix(*uploaded.LogoURL, orglogo.LogoPublicPathPrefix), *uploaded.LogoURL)

	served := env.do(http.MethodGet, *uploaded.LogoURL, "", "", nil)
	defer func() { _ = served.Body.Close() }()
	r.Equal(http.StatusOK, served.StatusCode, "the logo URL must be publicly fetchable with no signature")
	r.Equal("image/png", served.Header.Get("Content-Type"))
	r.Equal("nosniff", served.Header.Get("X-Content-Type-Options"))
	r.Contains(served.Header.Get("Content-Disposition"), "inline", "a raster logo renders inline")

	pngURL := *uploaded.LogoURL

	// Replacing with an SVG: accepted, but never served as an inline document.
	// An uploaded SVG is XML that can carry <script>; served inline on our own
	// origin that is stored XSS. attachment + nosniff kills that while <img>
	// rendering (which never executes script) keeps working.
	svg := env.uploadLogo(env.ownerJWT, "logo.svg", "image/svg+xml",
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	defer func() { _ = svg.Body.Close() }()
	r.Equal(http.StatusOK, svg.StatusCode)

	r.NoError(json.NewDecoder(svg.Body).Decode(&uploaded))
	r.NotNil(uploaded.LogoURL)
	r.NotEqual(pngURL, *uploaded.LogoURL)

	svgURL := *uploaded.LogoURL

	servedSVG := env.do(http.MethodGet, *uploaded.LogoURL, "", "", nil)
	defer func() { _ = servedSVG.Body.Close() }()
	r.Equal(http.StatusOK, servedSVG.StatusCode)
	r.Equal("image/svg+xml", servedSVG.Header.Get("Content-Type"))
	r.Equal("nosniff", servedSVG.Header.Get("X-Content-Type-Options"))
	r.Contains(servedSVG.Header.Get("Content-Disposition"), "attachment",
		"an uploaded SVG must never be served as an inline document on our origin")

	// The replaced PNG is retired: its public URL stops resolving immediately.
	retired := env.do(http.MethodGet, pngURL, "", "", nil)
	defer func() { _ = retired.Body.Close() }()
	r.Equal(http.StatusNotFound, retired.StatusCode,
		"a replaced logo must stop being publicly served")

	// Clearing drops the logo and un-publishes the current file too.
	cleared := env.do(http.MethodDelete, "/api/v1/orgs/"+env.orgSlug+"/logo", env.ownerJWT, "", nil)
	defer func() { _ = cleared.Body.Close() }()
	r.Equal(http.StatusOK, cleared.StatusCode)

	r.NoError(json.NewDecoder(cleared.Body).Decode(&uploaded))
	r.Nil(uploaded.LogoURL)

	gone := env.do(http.MethodGet, svgURL, "", "", nil)
	defer func() { _ = gone.Body.Close() }()
	r.Equal(http.StatusNotFound, gone.StatusCode)
}

// TestOrgLogoLegacyRouteIsRetired pins the decision in spec 2026-08-22-03's
// resolved question 1: /pub/org-logos/ is not kept alive alongside the generic
// route. Leaving it registered would leave a second copy of the pattern — and
// a second access check to keep in step — which is the thing the spec removes.
func TestOrgLogoLegacyRouteIsRetired(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newProfileEnv(t)

	png := env.uploadLogo(env.ownerJWT, "logo.png", "image/png", []byte("\x89PNG\r\n\x1a\nfake"))
	defer func() { _ = png.Body.Close() }()
	r.Equal(http.StatusOK, png.StatusCode)

	var uploaded struct {
		LogoURL *string `json:"logoUrl"`
	}

	r.NoError(json.NewDecoder(png.Body).Decode(&uploaded))
	r.NotNil(uploaded.LogoURL)
	r.True(strings.HasPrefix(*uploaded.LogoURL, files.PublicAssetPathPrefix),
		"an uploaded logo must be addressed by the generic public asset route: %s", *uploaded.LogoURL)

	fileUID := strings.TrimPrefix(*uploaded.LogoURL, files.PublicAssetPathPrefix)

	// Positive control: the new route serves it.
	served := env.do(http.MethodGet, *uploaded.LogoURL, "", "", nil)
	defer func() { _ = served.Body.Close() }()
	r.Equal(http.StatusOK, served.StatusCode)

	// The route is unregistered, so what answers is whatever catches unknown
	// paths — never the image. Asserting on the CONTENT TYPE rather than the
	// status is what makes this test independent of the SPA fallback's shape.
	legacy := env.do(http.MethodGet, "/pub/org-logos/"+fileUID, "", "", nil)
	defer func() { _ = legacy.Body.Close() }()
	r.NotEqual("image/png", legacy.Header.Get("Content-Type"),
		"the bespoke org-logo route must be gone, not still streaming blobs")
}

// TestPublicAssetRouteRefusesANonAllowlistedFile is the closed-allowlist
// property at the HTTP layer: a perfectly real, live file that is not an
// allowlisted attachment must 404 on the unsigned route, not stream.
func TestPublicAssetRouteRefusesANonAllowlistedFile(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newProfileEnv(t)

	// A file with no topic at all — the shape every ordinary upload has.
	plain := models.NewFile(env.orgUID, "secret.png", "image/png", "local://x", 10, nil)
	r.NoError(env.dbService.CreateFile(t.Context(), plain))

	resp := env.do(http.MethodGet, files.PublicAssetPathPrefix+plain.UID, "", "", nil)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode, "a file with no attachment topic is private")

	// ...and one with a real but NON-public topic (an incident screenshot is
	// org-operational evidence, which must never be served unsigned).
	shot := models.NewFile(env.orgUID, "shot.png", "image/png", "local://x", 10, nil)
	topic := "incidents/" + plain.UID + "/screenshot"
	shot.Topic = &topic
	r.NoError(env.dbService.CreateFile(t.Context(), shot))

	shotResp := env.do(http.MethodGet, files.PublicAssetPathPrefix+shot.UID, "", "", nil)
	defer func() { _ = shotResp.Body.Close() }()
	r.Equal(http.StatusNotFound, shotResp.StatusCode, "an incident screenshot is not a public asset")
}
