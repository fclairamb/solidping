package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/authhandoff"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portHandoffExchangePG is distinct from every other embedded-Postgres port
// claimed in the repo (see the port-numbering note in
// internal/db/incident_number_test.go).
const portHandoffExchangePG = 15549

// exchangeCase is one exchange-endpoint behavior, run on both engines.
type exchangeCase struct {
	name string
	run  func(t *testing.T, svc *Service, dbSvc db.Service)
}

func exchangeCases() []exchangeCase {
	return []exchangeCase{
		{"org session: login shape, then single use", testExchangeOrgSession},
		{"org-less pending session", testExchangeOrgLessSession},
		{"failures share one 401 body", testExchangeFailuresAreIndistinguishable},
		{"malformed body", testExchangeMalformedBody},
	}
}

func TestHandoffExchange_SQLite(t *testing.T) {
	t.Parallel()

	for _, tc := range exchangeCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, dbSvc, _ := setupAuthTestService(t)
			tc.run(t, svc, dbSvc)
		})
	}
}

//nolint:paralleltest,tparallel // one embedded PG instance shared by every sub-test
func TestHandoffExchange_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portHandoffExchangePG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	authCfg := config.AuthConfig{
		JWTSecret:          "test-jwt-secret",
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}
	svc := NewService(dbSvc, authCfg, &config.Config{Auth: authCfg}, nil, nil)

	for _, tc := range exchangeCases() {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, svc, dbSvc)
		})
	}
}

// exchangeFixture is a member (owner) of an org, unique per call so the
// Postgres cases can share one database.
func exchangeFixture(ctx context.Context, t *testing.T, dbSvc db.Service) (*models.User, *models.Organization) {
	t.Helper()

	fixture := nextFixture()
	org := joinTestOrg(ctx, t, dbSvc, "handoff-"+fixture, false)
	user := joinTestUser(ctx, t, dbSvc, "handoff-"+fixture+"@acme.com")

	require.NoError(t, dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleOwner)))

	return user, org
}

// postExchange calls the real handler and returns the recorder.
func postExchange(t *testing.T, svc *Service, body string) *httptest.ResponseRecorder {
	t.Helper()

	handler := NewHandler(svc, &config.Config{})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/api/v1/auth/handoff/exchange", strings.NewReader(body))
	rec := httptest.NewRecorder()

	require.NoError(t, handler.ExchangeHandoff(rec, req))

	return rec
}

func codeBody(t *testing.T, code string) string {
	t.Helper()

	raw, err := json.Marshal(HandoffExchangeRequest{Code: code})
	require.NoError(t, err)

	return string(raw)
}

// testExchangeOrgSession: the exchange answers what the old redirect URL
// carried (access_token, refresh_token, expires_in, org) inside the login
// response shape the dashboard already knows, exactly once.
func testExchangeOrgSession(t *testing.T, svc *Service, dbSvc db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	user, org := exchangeFixture(ctx, t, dbSvc)

	tokens, err := svc.GenerateTokensForOAuth(ctx, user, org, string(models.MemberRoleOwner), "google", Context{})
	r.NoError(err)

	code, err := authhandoff.Issue(ctx, dbSvc, &authhandoff.Session{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
		UserUID:      user.UID,
		OrgSlug:      org.Slug,
		ReturnTo:     "/d/orgs/" + org.Slug + "/checks",
	})
	r.NoError(err)

	rec := postExchange(t, svc, codeBody(t, code))
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Equal("no-store", rec.Header().Get("Cache-Control"))

	var resp HandoffExchangeResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))

	// The session the old URL carried, unchanged.
	r.Equal(tokens.AccessToken, resp.AccessToken)
	r.Equal(tokens.RefreshToken, resp.RefreshToken)
	r.Equal(tokens.ExpiresIn, resp.ExpiresIn)
	r.NotNil(resp.Organization)
	r.Equal(org.Slug, resp.Organization.Slug)

	// ...in the password-login shape.
	r.Equal(tokenTypeBearer, resp.TokenType)
	r.NotNil(resp.User)
	r.Equal(user.UID, resp.User.UID)
	r.Equal(string(models.MemberRoleOwner), resp.User.Role)
	r.Equal(LoginActionDefault, resp.LoginAction)
	r.Len(resp.Organizations, 1)
	r.Equal(org.Slug, resp.Organizations[0].Slug)
	r.Equal("/d/orgs/"+org.Slug+"/checks", resp.ReturnTo)
	r.Empty(resp.MembershipPending)

	// The raw JSON uses the login response's field names.
	var raw map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &raw))

	for _, key := range []string{"accessToken", "refreshToken", "expiresIn", "user", "organization", "organizations"} {
		r.Contains(raw, key)
	}

	// The SPA cookie follows the adopted session, like Login and Refresh.
	var cookie *http.Cookie

	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieAuthToken {
			cookie = c
		}
	}

	r.NotNil(cookie)
	r.Equal(tokens.AccessToken, cookie.Value)

	// The minted access token is a real one.
	claims, err := svc.ValidateToken(ctx, resp.AccessToken)
	r.NoError(err)
	r.Equal(org.Slug, claims.OrgSlug)

	// Single use.
	second := postExchange(t, svc, codeBody(t, code))
	r.Equal(http.StatusUnauthorized, second.Code)
}

// testExchangeOrgLessSession: a login the org did not admit hands over an
// org-less session (no refresh token, no organization) and names the org.
func testExchangeOrgLessSession(t *testing.T, svc *Service, dbSvc db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	fixture := nextFixture()
	user := joinTestUser(ctx, t, dbSvc, "pending-"+fixture+"@acme.com")

	pending, err := svc.pendingSession(ctx, user)
	r.NoError(err)

	code, err := authhandoff.Issue(ctx, dbSvc, (&ProviderOutcome{
		AccessToken:    pending.AccessToken,
		ExpiresIn:      pending.ExpiresIn,
		OrgSlug:        "acme",
		UserUID:        user.UID,
		Pending:        true,
		PendingOrgSlug: "acme",
	}).handoffSession("/d/orgs/acme"))
	r.NoError(err)

	rec := postExchange(t, svc, codeBody(t, code))
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp HandoffExchangeResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))

	r.Equal(pending.AccessToken, resp.AccessToken)
	r.Empty(resp.RefreshToken, "an org-less session has no refresh token")
	r.Equal(pending.ExpiresIn, resp.ExpiresIn)
	r.Nil(resp.Organization)
	r.Equal(LoginActionNoOrg, resp.LoginAction)
	r.Equal("acme", resp.MembershipPending)
	r.NotNil(resp.User)
	r.Equal(user.UID, resp.User.UID)

	claims, err := svc.ValidateToken(ctx, resp.AccessToken)
	r.NoError(err)
	r.Empty(claims.OrgSlug)
}

// testExchangeFailuresAreIndistinguishable: reused, expired, unknown and
// forged codes all get the same 401 body, byte for byte.
func testExchangeFailuresAreIndistinguishable(t *testing.T, svc *Service, dbSvc db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	user, org := exchangeFixture(ctx, t, dbSvc)

	issue := func() string {
		code, err := authhandoff.Issue(ctx, dbSvc, &authhandoff.Session{
			AccessToken: "at", RefreshToken: "rt", ExpiresIn: 3600, UserUID: user.UID, OrgSlug: org.Slug,
		})
		r.NoError(err)

		return code
	}

	// Rewrites the stored row, keeping its hash, so the code still finds it.
	rewrite := func(code string, mutate func(*models.AuthHandoffCode)) {
		row, err := dbSvc.ConsumeAuthHandoffCode(ctx, authhandoff.HashCode(code))
		r.NoError(err)
		mutate(row)
		r.NoError(dbSvc.CreateAuthHandoffCode(ctx, row))
	}

	reused := issue()
	r.Equal(http.StatusOK, postExchange(t, svc, codeBody(t, reused)).Code)

	expired := issue()
	rewrite(expired, func(row *models.AuthHandoffCode) { row.ExpiresAt = time.Now().Add(-time.Second) })

	forged := issue()
	rewrite(forged, func(row *models.AuthHandoffCode) {
		row.Payload = strings.ToUpper(row.Payload[:10]) + row.Payload[10:]
	})

	bodies := map[string][]byte{}

	for name, code := range map[string]string{
		"reused":  reused,
		"expired": expired,
		"unknown": "c29tZXRoaW5nLXRoYXQtd2FzLW5ldmVyLWlzc3VlZC1hdC1hbGw",
		"forged":  forged,
		"empty":   "",
	} {
		rec := postExchange(t, svc, codeBody(t, code))
		r.Equal(http.StatusUnauthorized, rec.Code, name)
		r.Empty(rec.Result().Cookies(), "%s: a refused exchange sets no cookie", name)
		bodies[name] = rec.Body.Bytes()
	}

	for name, body := range bodies {
		r.True(bytes.Equal(bodies["unknown"], body), "%s body differs: %s vs %s", name, body, bodies["unknown"])
	}

	r.Contains(string(bodies["unknown"]), `"code":"UNAUTHORIZED"`)
}

// testExchangeMalformedBody: not JSON at all is a validation error (422), not a 500.
func testExchangeMalformedBody(t *testing.T, svc *Service, _ db.Service) {
	t.Helper()

	rec := postExchange(t, svc, "not json")
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}
