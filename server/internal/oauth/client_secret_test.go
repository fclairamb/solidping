package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// setupClientSecretFixture is setupOAuthService with one difference: the
// caller controls oauth.enforce_client_secret directly, because
// setupOAuthService's config has no OAuth section (so it defaults to the Go
// zero value, false) and every test in this file cares specifically about
// that flag. A confidential client is pre-registered alongside the usual
// public one.
func setupClientSecretFixture(t *testing.T, enforceClientSecret bool) (oauthFixture, *models.OAuthClient, string) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	fullCfg := &config.Config{
		Server: config.ServerConfig{BaseURL: oauthTestIssuer},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		OAuth: config.OAuthConfig{EnforceClientSecret: enforceClientSecret},
	}

	authSvc := auth.NewService(dbService, fullCfg.Auth, fullCfg, nil, nil)
	oauthSvc := NewService(dbService, authSvc, fullCfg)

	org := models.NewOrganization("oauth-secret-org", "")
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	user := models.NewUser("oauth-secret@example.com")
	require.NoError(t, dbService.CreateUser(ctx, user))

	pubClient, _, err := oauthSvc.RegisterClient(
		ctx, "Public Test Client",
		[]string{testRedirectURI},
		[]string{GrantAuthorizationCode, GrantRefreshToken},
		[]string{ScopeMCP}, true,
	)
	require.NoError(t, err)

	confClient, confSecret, err := oauthSvc.RegisterClient(
		ctx, "Confidential Test Client",
		[]string{testRedirectURI},
		[]string{GrantAuthorizationCode, GrantRefreshToken},
		[]string{ScopeMCP}, false,
	)
	require.NoError(t, err)
	require.NotEmpty(t, confSecret)

	f := oauthFixture{
		svc: oauthSvc, authSvc: authSvc, db: dbService,
		org: org, user: user, client: pubClient, ctx: ctx,
	}

	return f, confClient, confSecret
}

// issueCodeFor issues a fresh single-use PKCE-bound authorization code for the
// given client, redeemable by testVerifier.
func (f oauthFixture) issueCodeFor(t *testing.T, clientID string) string {
	t.Helper()

	code, err := f.svc.IssueAuthCode(f.ctx, &AuthCodeGrant{
		ClientID:            clientID,
		UserUID:             f.user.UID,
		OrgUID:              f.org.UID,
		OrgSlug:             f.org.Slug,
		RedirectURI:         testRedirectURI,
		Scope:               ScopeMCP,
		Resource:            oauthTestIssuer + "/api/v1/mcp",
		CodeChallenge:       challengeFor(testVerifier),
		CodeChallengeMethod: CodeChallengeMethodS256,
	})
	require.NoError(t, err)

	return code
}

// tokenHandler builds a Handler over the fixture's service, mirroring
// oauthFixture.revokeHandler in revoke_test.go.
func (f oauthFixture) tokenHandler() *Handler {
	return NewHandler(f.svc, &config.Config{
		Server: config.ServerConfig{BaseURL: oauthTestIssuer},
	})
}

// postToken sends a form-encoded POST to the token endpoint, optionally with
// HTTP Basic credentials (client_secret_basic).
func postToken(t *testing.T, h *Handler, form url.Values, basicUser, basicPass string, useBasic bool) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost, PathToken, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if useBasic {
		req.SetBasicAuth(basicUser, basicPass)
	}

	rec := httptest.NewRecorder()
	require.NoError(t, h.Token(rec, req))

	return rec
}

func authCodeForm(code, clientID string, clientSecret string) url.Values {
	form := url.Values{}
	form.Set("grant_type", GrantAuthorizationCode)
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("redirect_uri", testRedirectURI)
	form.Set("code_verifier", testVerifier)

	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}

	return form
}

// TestTokenEndpointConfidentialClientSecretPost covers the client_secret_post
// form (RFC 6749 §2.3.1): a confidential client presenting its secret in the
// body gets a token.
func TestTokenEndpointConfidentialClientSecretPost(t *testing.T) {
	t.Parallel()

	f, confClient, secret := setupClientSecretFixture(t, true)
	h := f.tokenHandler()

	code := f.issueCodeFor(t, confClient.ClientID)
	rec := postToken(t, h, authCodeForm(code, confClient.ClientID, secret), "", "", false)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var tok tokenResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tok))
	require.NotEmpty(t, tok.AccessToken)
}

// TestTokenEndpointConfidentialClientSecretBasic covers the
// client_secret_basic form: the same client authenticating via HTTP Basic
// instead of a body field.
func TestTokenEndpointConfidentialClientSecretBasic(t *testing.T) {
	t.Parallel()

	f, confClient, secret := setupClientSecretFixture(t, true)
	h := f.tokenHandler()

	code := f.issueCodeFor(t, confClient.ClientID)

	// client_id is intentionally omitted from the body: Basic carries it.
	form := url.Values{}
	form.Set("grant_type", GrantAuthorizationCode)
	form.Set("code", code)
	form.Set("redirect_uri", testRedirectURI)
	form.Set("code_verifier", testVerifier)

	rec := postToken(t, h, form, confClient.ClientID, secret, true)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var tok tokenResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tok))
	require.NotEmpty(t, tok.AccessToken)
}

// TestTokenEndpointConfidentialClientWrongSecret asserts the hard-401 default
// (oauth.enforce_client_secret=true): a confidential client with a wrong
// secret is rejected, and the auth code is NOT burned by the attempt (the
// client authentication check runs before the grant is consumed), matching
// the existing single-use-code contract.
func TestTokenEndpointConfidentialClientWrongSecret(t *testing.T) {
	t.Parallel()

	f, confClient, secret := setupClientSecretFixture(t, true)
	h := f.tokenHandler()

	code := f.issueCodeFor(t, confClient.ClientID)
	rec := postToken(t, h, authCodeForm(code, confClient.ClientID, "definitely-not-the-secret"), "", "", false)

	require.Equal(t, http.StatusUnauthorized, rec.Code)

	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, ErrInvalidClient, body.Error)

	// The code must still be redeemable with the correct secret — a failed
	// auth attempt must not have consumed it.
	rec2 := postToken(t, h, authCodeForm(code, confClient.ClientID, secret), "", "", false)
	require.Equal(t, http.StatusOK, rec2.Code, "body: %s", rec2.Body.String())
}

// TestTokenEndpointConfidentialClientMissingSecret covers the missing-secret
// case distinctly from a wrong one.
func TestTokenEndpointConfidentialClientMissingSecret(t *testing.T) {
	t.Parallel()

	f, confClient, _ := setupClientSecretFixture(t, true)
	h := f.tokenHandler()

	code := f.issueCodeFor(t, confClient.ClientID)
	rec := postToken(t, h, authCodeForm(code, confClient.ClientID, ""), "", "", false)

	require.Equal(t, http.StatusUnauthorized, rec.Code)

	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, ErrInvalidClient, body.Error)
}

// TestTokenEndpointBadSecretMatchesUnknownClient is the anti-enumeration
// guard the spec calls for: a confidential client's wrong secret and a
// nonexistent client_id must produce byte-identical responses, or the token
// endpoint becomes an oracle for which client IDs are registered.
func TestTokenEndpointBadSecretMatchesUnknownClient(t *testing.T) {
	t.Parallel()

	f, confClient, _ := setupClientSecretFixture(t, true)
	h := f.tokenHandler()

	badSecretCode := f.issueCodeFor(t, confClient.ClientID)
	badSecretRec := postToken(t, h, authCodeForm(badSecretCode, confClient.ClientID, "wrong"), "", "", false)
	require.Equal(t, http.StatusUnauthorized, badSecretRec.Code)

	unknownClientRec := postToken(t, h, authCodeForm("irrelevant-code", "no-such-client-id", ""), "", "", false)
	require.Equal(t, http.StatusUnauthorized, unknownClientRec.Code)

	require.Equal(t, badSecretRec.Body.String(), unknownClientRec.Body.String(),
		"a bad secret must read identically to an unknown client_id")
}

// TestTokenEndpointPublicClientSecretIgnored covers proposal item 4's
// requirement in miniature: a public client is never asked to authenticate,
// so a bogus client_secret alongside it changes nothing.
func TestTokenEndpointPublicClientSecretIgnored(t *testing.T) {
	t.Parallel()

	f, _, _ := setupClientSecretFixture(t, true)
	h := f.tokenHandler()

	code := f.issueCodeFor(t, f.client.ClientID)
	rec := postToken(t, h, authCodeForm(code, f.client.ClientID, "this-is-not-checked"), "", "", false)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}

// TestTokenEndpointEnforceClientSecretFalseIsLogOnly exercises the
// oauth.enforce_client_secret escape hatch: with it off, a confidential
// client presenting a wrong or missing secret still gets a token (the
// failure is only logged, once per client ID per process — see
// Service.warnClientAuthFailureOnce), for migrating a client that was
// registered before verification existed.
func TestTokenEndpointEnforceClientSecretFalseIsLogOnly(t *testing.T) {
	t.Parallel()

	f, confClient, _ := setupClientSecretFixture(t, false)
	h := f.tokenHandler()

	code1 := f.issueCodeFor(t, confClient.ClientID)
	rec1 := postToken(t, h, authCodeForm(code1, confClient.ClientID, "wrong-secret"), "", "", false)
	require.Equal(t, http.StatusOK, rec1.Code, "body: %s", rec1.Body.String())

	// A second failure for the same client (past the "first time" the WARN
	// fires) must still let the request through — enforcement, not logging,
	// is what gates the 401.
	code2 := f.issueCodeFor(t, confClient.ClientID)
	rec2 := postToken(t, h, authCodeForm(code2, confClient.ClientID, ""), "", "", false)
	require.Equal(t, http.StatusOK, rec2.Code, "body: %s", rec2.Body.String())
}

// TestAuthenticateClientDirect exercises Service.AuthenticateClient without
// the HTTP layer, pinning the exact contract token.go relies on.
func TestAuthenticateClientDirect(t *testing.T) {
	t.Parallel()

	f, confClient, secret := setupClientSecretFixture(t, true)

	require.NoError(t, f.svc.AuthenticateClient(f.ctx, f.client.ClientID, ""),
		"a public client needs no secret")
	require.NoError(t, f.svc.AuthenticateClient(f.ctx, f.client.ClientID, "anything"),
		"a public client's secret, if presented, is ignored")

	require.NoError(t, f.svc.AuthenticateClient(f.ctx, confClient.ClientID, secret))

	err := f.svc.AuthenticateClient(f.ctx, confClient.ClientID, "wrong")
	require.ErrorIs(t, err, errClientAuthFailed)

	err = f.svc.AuthenticateClient(f.ctx, "no-such-client", "whatever")
	require.ErrorIs(t, err, errClientNotFound)
}

// TestRegisterConfidentialClientHTTPBothAuthMethods drives the RFC 7591
// registration endpoint over HTTP for both confidential auth methods and for
// "none", and confirms the stored secret is hashed, never the raw value.
func TestRegisterConfidentialClientHTTPBothAuthMethods(t *testing.T) {
	t.Parallel()

	h := setupOAuthHandler(t)

	cases := []struct {
		name           string
		authMethod     string
		wantSecret     bool
		wantAuthMethod string
	}{
		{"post", AuthMethodSecretPost, true, AuthMethodSecretPost},
		{"basic", AuthMethodSecretBasic, true, AuthMethodSecretBasic},
		{"none", AuthMethodNone, false, AuthMethodNone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body := `{"redirect_uris":["` + testRedirectURI + `"],"client_name":"` + tc.name +
				`","token_endpoint_auth_method":"` + tc.authMethod + `"}`
			r := httptest.NewRequestWithContext(
				context.Background(), http.MethodPost, PathRegister, strings.NewReader(body))
			rec := httptest.NewRecorder()
			require.NoError(t, h.Register(rec, r))

			require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

			var resp registrationResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.Equal(t, tc.wantAuthMethod, resp.TokenEndpointAuthMethod)

			if tc.wantSecret {
				require.NotEmpty(t, resp.ClientSecret)

				stored, err := h.svc.GetClient(context.Background(), resp.ClientID)
				require.NoError(t, err)
				require.NotNil(t, stored.SecretHash)
				require.NotEqual(t, resp.ClientSecret, *stored.SecretHash,
					"the stored secret must be hashed, never the raw value")
			} else {
				require.Empty(t, resp.ClientSecret)
			}
		})
	}
}

// TestRegisterEndpointRejectsUnknownAuthMethod pins the new validation: a
// nonsense token_endpoint_auth_method must be rejected rather than silently
// treated as confidential.
func TestRegisterEndpointRejectsUnknownAuthMethod(t *testing.T) {
	t.Parallel()

	h := setupOAuthHandler(t)

	body := `{"redirect_uris":["` + testRedirectURI + `"],"token_endpoint_auth_method":"private_key_jwt"}`
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, PathRegister, strings.NewReader(body))
	rec := httptest.NewRecorder()
	require.NoError(t, h.Register(rec, r))

	require.Equal(t, http.StatusBadRequest, rec.Code)

	var body2 errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body2))
	require.Equal(t, ErrInvalidClientMetadata, body2.Error)
}

// TestAuthorizationServerMetadataAdvertisesBothSecretMethods pins the
// metadata fix alongside the enforcement fix: advertising client_secret_post
// without checking it was the original bug, so both confidential methods must
// be listed now that both are checked.
func TestAuthorizationServerMetadataAdvertisesBothSecretMethods(t *testing.T) {
	t.Parallel()

	h := setupOAuthHandler(t)
	doc := h.metadata().BuildAuthorizationServerMetadata()

	require.Contains(t, doc.TokenEndpointAuthMethodsSupported, AuthMethodSecretPost)
	require.Contains(t, doc.TokenEndpointAuthMethodsSupported, AuthMethodSecretBasic)
	require.Contains(t, doc.TokenEndpointAuthMethodsSupported, AuthMethodNone)
}
