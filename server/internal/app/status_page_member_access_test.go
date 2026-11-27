package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/statuspagekiosk"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// memberAccessFixture is a real server with two orgs, one member in each, and
// a `private`, a `password` and a `public` page in the first org.
type memberAccessFixture struct {
	ts          *httptest.Server
	orgSlug     string
	member      string // access token of a member of orgSlug
	otherMember string // access token of a member of another org
	kioskToken  string // valid kiosk token of the private page
}

const (
	memberPrivateSlug  = "internal"
	memberPasswordSlug = "partners"
	memberPublicSlug   = "public"
)

func newMemberAccessFixture(t *testing.T) *memberAccessFixture {
	t.Helper()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "status-page-member-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour
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

	dbSvc := server.dbService

	const password = "member-access-pass"

	hash, err := passwords.Hash(password)
	r.NoError(err)

	now := time.Now()

	addMember := func(org *models.Organization, email string, role models.MemberRole) {
		user := models.NewUser(email)
		user.PasswordHash = &hash
		user.EmailVerifiedAt = &now
		r.NoError(dbSvc.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, role)
		member.JoinedAt = &now
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))
	}

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	other := models.NewOrganization("other", "Other")
	r.NoError(dbSvc.CreateOrganization(ctx, other))

	// The lowest role: every role reads status pages.
	addMember(org, "viewer@acme.com", models.MemberRoleViewer)
	addMember(other, "someone@other.com", models.MemberRoleAdmin)

	login := func(orgSlug, email string) string {
		payload, marshalErr := json.Marshal(map[string]string{"org": orgSlug, "email": email, "password": password})
		r.NoError(marshalErr)

		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/v1/auth/login",
			bytes.NewReader(payload))
		r.NoError(reqErr)
		req.Header.Set("Content-Type", "application/json")

		resp, doErr := http.DefaultClient.Do(req)
		r.NoError(doErr)

		defer func() { _ = resp.Body.Close() }()

		r.Equal(http.StatusOK, resp.StatusCode)

		var out struct {
			AccessToken string `json:"accessToken"`
		}
		r.NoError(json.NewDecoder(resp.Body).Decode(&out))
		r.NotEmpty(out.AccessToken)

		return out.AccessToken
	}

	kioskToken, kioskHash, err := statuspagekiosk.Generate()
	r.NoError(err)

	private := models.NewStatusPage(org.UID, "Internal", memberPrivateSlug)
	private.Visibility = models.StatusPageVisibilityPrivate
	private.KioskTokenHash = &kioskHash
	r.NoError(dbSvc.CreateStatusPage(ctx, private))

	pagePassword, err := passwords.Hash("partner-secret")
	r.NoError(err)

	locked := models.NewStatusPage(org.UID, "Partners", memberPasswordSlug)
	locked.Visibility = models.StatusPageVisibilityPassword
	locked.PasswordHash = &pagePassword
	r.NoError(dbSvc.CreateStatusPage(ctx, locked))

	public := models.NewStatusPage(org.UID, "Public", memberPublicSlug)
	r.NoError(dbSvc.CreateStatusPage(ctx, public))

	return &memberAccessFixture{
		ts:          ts,
		orgSlug:     org.Slug,
		member:      login(org.Slug, "viewer@acme.com"),
		otherMember: login(other.Slug, "someone@other.com"),
		kioskToken:  kioskToken,
	}
}

// caller describes how a request authenticates.
type caller struct {
	cookie string
	bearer string
	kiosk  string
}

// memberResponse is what the assertions read of a response, whose body is
// already drained and closed.
type memberResponse struct {
	StatusCode int
	Header     http.Header
}

func (f *memberAccessFixture) get(t *testing.T, path string, who caller) memberResponse {
	t.Helper()

	url := f.ts.URL + path
	if who.kiosk != "" {
		url += "?kiosk=" + who.kiosk
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)

	if who.cookie != "" {
		req.AddCookie(&http.Cookie{Name: middleware.CookieAuthToken, Value: who.cookie})
	}

	if who.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+who.bearer)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	return memberResponse{StatusCode: resp.StatusCode, Header: resp.Header}
}

// surfaces are every public read of a page that goes through the gate.
func memberSurfaces(orgSlug, slug string) map[string]string {
	base := "/api/v1/status-pages/" + orgSlug + "/" + slug

	return map[string]string{
		"view":      base,
		"summary":   base + "/summary",
		"badge":     base + "/badge",
		"feed":      base + "/feed.xml",
		"incidents": base + "/incidents",
	}
}

// TestPrivateStatusPageMemberAccess — spec 2026-11-27-01: a signed-in member of
// the page's organization reads its `private` page on every public surface,
// with a cookie or a bearer token; everybody else gets the same 404 an
// anonymous visitor gets, an invalid session included (404, never 401).
func TestPrivateStatusPageMemberAccess(t *testing.T) {
	t.Parallel()

	f := newMemberAccessFixture(t)

	callers := []struct {
		name string
		who  caller
		want int
	}{
		{"anonymous", caller{}, http.StatusNotFound},
		{"member cookie", caller{cookie: f.member}, http.StatusOK},
		{"member bearer", caller{bearer: f.member}, http.StatusOK},
		{"member of another org", caller{cookie: f.otherMember}, http.StatusNotFound},
		{"member of another org bearer", caller{bearer: f.otherMember}, http.StatusNotFound},
		{"invalid cookie", caller{cookie: "not-a-jwt"}, http.StatusNotFound},
		{"invalid bearer", caller{bearer: "not-a-jwt"}, http.StatusNotFound},
		{"revoked kiosk token", caller{kiosk: "revoked-or-wrong-token"}, http.StatusNotFound},
		{"valid kiosk token", caller{kiosk: f.kioskToken}, http.StatusOK},
	}

	for surface, path := range memberSurfaces(f.orgSlug, memberPrivateSlug) {
		for _, tc := range callers {
			t.Run(surface+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				resp := f.get(t, path, tc.who)
				require.Equal(t, tc.want, resp.StatusCode)
				// Allowed or not, nothing shared may store the answer.
				require.Equal(t, "private, no-store", resp.Header.Get("Cache-Control"))

				if tc.want == http.StatusOK {
					vary := resp.Header.Get("Vary")
					require.Contains(t, vary, "Cookie")
					require.Contains(t, vary, "Authorization")
				}
			})
		}
	}
}

// TestPasswordStatusPageMemberSkipsTheUnlock — a member does not have to type
// the password of their own page; anybody else still gets the 401.
func TestPasswordStatusPageMemberSkipsTheUnlock(t *testing.T) {
	t.Parallel()

	f := newMemberAccessFixture(t)

	for surface, path := range memberSurfaces(f.orgSlug, memberPasswordSlug) {
		t.Run(surface, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			r.Equal(http.StatusUnauthorized, f.get(t, path, caller{}).StatusCode)
			r.Equal(http.StatusUnauthorized, f.get(t, path, caller{cookie: f.otherMember}).StatusCode)
			r.Equal(http.StatusUnauthorized, f.get(t, path, caller{cookie: "stale"}).StatusCode)

			resp := f.get(t, path, caller{cookie: f.member})
			r.Equal(http.StatusOK, resp.StatusCode)
			r.Equal("private, no-store", resp.Header.Get("Cache-Control"))
		})
	}
}

// TestPublicStatusPageIgnoresTheSession — a public page keeps its public
// caching whoever asks, and a stale dashboard cookie never locks an anonymous
// visitor out of it.
func TestPublicStatusPageIgnoresTheSession(t *testing.T) {
	t.Parallel()

	f := newMemberAccessFixture(t)
	path := "/api/v1/status-pages/" + f.orgSlug + "/" + memberPublicSlug

	for _, who := range []caller{{}, {cookie: f.member}, {cookie: "stale"}, {bearer: "stale"}} {
		resp := f.get(t, path, who)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.True(t, strings.HasPrefix(resp.Header.Get("Cache-Control"), "public, max-age="),
			resp.Header.Get("Cache-Control"))
		require.NotContains(t, resp.Header.Get("Vary"), "Cookie")
	}
}
