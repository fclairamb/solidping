package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// createOrgResponse mirrors auth.OrgResponse for decoding the raw JSON body —
// POST /api/v1/orgs isn't in the generated OpenAPI client.
type createOrgResponse struct {
	UID          string `json:"uid"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	TokenType    string `json:"tokenType"`
}

// doBearerRequest issues a request with an arbitrary bearer token (unlike
// authedRequestWithBody, which is hardcoded to the fixture PAT) and returns
// the raw response body for callers that need a shape not covered by the
// generated client.
func doBearerRequest(t *testing.T, ts *TestServer, token, method, path string, body any) (int, []byte) {
	t.Helper()

	ctx := t.Context()

	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, ts.HTTPServer.URL+path, bodyReader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, data
}

// TestCreateOrgMintsScopedSession covers the 2026-07-08 "create-org missing
// org-scoped token" fix: a user with zero org memberships logs in (landing
// on the completeLogin zero-org branch, whose access token has orgSlug ""),
// creates their first org, and must receive a token scoped to that new org
// — not be left holding the no-org token, which 403s on every org-scoped
// route.
func TestCreateOrgMintsScopedSession(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	testServer := NewTestServer(t)
	dbService := testServer.Server.DBService()

	// A brand-new user with zero org memberships.
	passwordHash, err := passwords.Hash("testpassword123")
	r.NoError(err)

	user := models.NewUser("zeroorg@example.com")
	user.PasswordHash = &passwordHash
	r.NoError(dbService.CreateUser(ctx, user))

	// Login with no org — completeLogin's resolvedOrg == nil branch: the
	// access token it mints has orgSlug "".
	apiClient := testServer.NewClient()

	loginResp, err := apiClient.Login(ctx, "", "zeroorg@example.com", "testpassword123")
	r.NoError(err)
	r.NotNil(loginResp.AccessToken)

	noOrgToken := *loginResp.AccessToken

	// Sanity check: the no-org token can't reach any org-scoped route yet.
	status, body := doBearerRequest(t, testServer, noOrgToken, http.MethodGet,
		"/api/v1/orgs/"+TestOrgSlug+"/jobs", nil)
	r.Equal(http.StatusForbidden, status, "no-org token on existing org: body=%s", body)

	// Create the user's first org using the no-org token.
	status, data := doBearerRequest(t, testServer, noOrgToken, http.MethodPost, "/api/v1/orgs",
		map[string]any{"name": "Zero Org Co", "slug": "zero-org-co"})
	r.Equal(http.StatusCreated, status, "create org: body=%s", data)

	var created createOrgResponse
	r.NoError(json.Unmarshal(data, &created))
	r.Equal("zero-org-co", created.Slug)
	r.Equal("Zero Org Co", created.Name)
	r.NotEmpty(created.UID)
	r.NotEmpty(created.AccessToken)
	r.NotEmpty(created.RefreshToken)
	r.Positive(created.ExpiresIn)
	r.Equal("Bearer", created.TokenType)
	r.NotEqual(noOrgToken, created.AccessToken)

	// The NEW token is scoped to the new org: an org-scoped call succeeds
	// (2xx instead of the pre-fix 403) — this is the orgSlug claim check in
	// middleware/auth.go, exercised end-to-end rather than by decoding the
	// JWT directly.
	status, body = doBearerRequest(t, testServer, created.AccessToken, http.MethodGet,
		"/api/v1/orgs/"+created.Slug+"/jobs", nil)
	r.Equal(http.StatusOK, status, "new token on new org: body=%s", body)

	// The ORIGINAL no-org token still 403s on the new org — creating an org
	// doesn't retroactively fix the old token; the frontend must adopt the
	// new one (which is exactly the bug: the frontend used to navigate
	// straight to the new org without ever picking up a fresh token).
	status, body = doBearerRequest(t, testServer, noOrgToken, http.MethodGet,
		"/api/v1/orgs/"+created.Slug+"/jobs", nil)
	r.Equal(http.StatusForbidden, status, "old no-org token on new org: body=%s", body)
}

// TestCreateOrgSuperAdminUnaffected verifies minting a session for CreateOrg
// doesn't change behavior for super admins: they bypass the org-slug claims
// check already (middleware/auth.go IsSuperAdmin), so a correctly-scoped
// token here is a no-op improvement, not a behavior change — the call must
// still succeed and return usable org identity plus a session.
func TestCreateOrgSuperAdminUnaffected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	testServer := NewTestServer(t)
	dbService := testServer.Server.DBService()

	passwordHash, err := passwords.Hash("testpassword123")
	r.NoError(err)

	admin := models.NewUser("superadmin@example.com")
	admin.PasswordHash = &passwordHash
	admin.SuperAdmin = true
	r.NoError(dbService.CreateUser(ctx, admin))

	apiClient := testServer.NewClient()

	loginResp, err := apiClient.Login(ctx, "", "superadmin@example.com", "testpassword123")
	r.NoError(err)
	r.NotNil(loginResp.AccessToken)

	token := *loginResp.AccessToken

	// Super admins bypass the claims check, so even the pre-creation token
	// can already reach any org — confirm that's still true post-fix.
	status, body := doBearerRequest(t, testServer, token, http.MethodGet,
		"/api/v1/orgs/"+TestOrgSlug+"/jobs", nil)
	r.Equal(http.StatusOK, status, "super admin pre-existing token on existing org: body=%s", body)

	status, data := doBearerRequest(t, testServer, token, http.MethodPost, "/api/v1/orgs",
		map[string]any{"name": "Super Admin Org", "slug": "super-admin-org"})
	r.Equal(http.StatusCreated, status, "create org: body=%s", data)

	var created createOrgResponse
	r.NoError(json.Unmarshal(data, &created))
	r.Equal("super-admin-org", created.Slug)
	r.NotEmpty(created.AccessToken)

	status, body = doBearerRequest(t, testServer, created.AccessToken, http.MethodGet,
		"/api/v1/orgs/"+created.Slug+"/jobs", nil)
	r.Equal(http.StatusOK, status, "new token on new org: body=%s", body)
}
