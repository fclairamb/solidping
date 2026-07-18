package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

func setupOAuthHandler(t *testing.T) *Handler {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: oauthTestIssuer},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
	}

	authSvc := auth.NewService(dbService, cfg.Auth, cfg, nil, nil)
	svc := NewService(dbService, authSvc, cfg)

	return NewHandler(svc, cfg)
}

func doGET(t *testing.T, h func(http.ResponseWriter, bunrouter.Request) error, path string) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	rec := httptest.NewRecorder()
	require.NoError(t, h(rec, bunrouter.NewRequest(r)))

	return rec
}

func TestProtectedResourceMetadataHTTP(t *testing.T) {
	t.Parallel()

	h := setupOAuthHandler(t)
	rec := doGET(t, h.ProtectedResourceMetadata, PathProtectedResourceMetadata)

	require.Equal(t, http.StatusOK, rec.Code)

	var doc ProtectedResourceMetadata
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, oauthTestIssuer+"/api/v1/mcp", doc.Resource)
	require.Equal(t, []string{oauthTestIssuer}, doc.AuthorizationServers)
}

func TestAuthorizationServerMetadataHTTP(t *testing.T) {
	t.Parallel()

	h := setupOAuthHandler(t)
	rec := doGET(t, h.AuthorizationServerMetadata, PathAuthorizationServerMetadata)

	require.Equal(t, http.StatusOK, rec.Code)

	var doc AuthorizationServerMetadata
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, oauthTestIssuer, doc.Issuer)
	require.Equal(t, []string{CodeChallengeMethodS256}, doc.CodeChallengeMethodsSupported)
	require.NotEmpty(t, doc.RegistrationEndpoint)
	require.Equal(t, oauthTestIssuer+"/api/v1/oauth/revoke", doc.RevocationEndpoint,
		"AS metadata must advertise the RFC 7009 revocation_endpoint")
}

func TestRegisterEndpointLoopbackPublicClient(t *testing.T) {
	t.Parallel()

	h := setupOAuthHandler(t)

	body := `{"redirect_uris":["http://127.0.0.1:1234/cb"],"client_name":"Native","token_endpoint_auth_method":"none"}`
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, PathRegister, strings.NewReader(body))
	rec := httptest.NewRecorder()
	require.NoError(t, h.Register(rec, bunrouter.NewRequest(r)))

	require.Equal(t, http.StatusCreated, rec.Code)

	var resp registrationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ClientID)
	require.Empty(t, resp.ClientSecret, "public client must not get a secret")
	require.Equal(t, "none", resp.TokenEndpointAuthMethod)
}

func TestRegisterEndpointRejectsNonLoopbackHTTP(t *testing.T) {
	t.Parallel()

	h := setupOAuthHandler(t)

	body := `{"redirect_uris":["http://evil.example.com/cb"]}`
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, PathRegister, strings.NewReader(body))
	rec := httptest.NewRecorder()
	require.NoError(t, h.Register(rec, bunrouter.NewRequest(r)))

	require.Equal(t, http.StatusBadRequest, rec.Code)

	var body2 errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body2))
	require.Equal(t, ErrInvalidRedirectURI, body2.Error)
}

func TestTokenEndpointUnsupportedGrant(t *testing.T) {
	t.Parallel()

	h := setupOAuthHandler(t)

	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, PathToken,
		strings.NewReader("grant_type=password"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	require.NoError(t, h.Token(rec, bunrouter.NewRequest(r)))

	require.Equal(t, http.StatusBadRequest, rec.Code)

	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, ErrUnsupportedGrantType, body.Error)
}

func TestTokenEndpointAuthCodeRequiresPKCE(t *testing.T) {
	t.Parallel()

	h := setupOAuthHandler(t)

	form := "grant_type=authorization_code&code=x&client_id=c&redirect_uri=http://127.0.0.1/cb"
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, PathToken, strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	require.NoError(t, h.Token(rec, bunrouter.NewRequest(r)))

	require.Equal(t, http.StatusBadRequest, rec.Code)

	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, ErrInvalidRequest, body.Error)
	require.Contains(t, body.ErrorDescription, "code_verifier")
}
