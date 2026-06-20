package oauth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

const testIssuer = "https://solidping.example.com"

func TestProtectedResourceMetadataWellFormed(t *testing.T) {
	t.Parallel()

	md := NewMetadata(testIssuer + "/") // trailing slash must be trimmed
	doc := md.BuildProtectedResourceMetadata()

	require.Equal(t, testIssuer+"/api/v1/mcp", doc.Resource)
	require.Equal(t, []string{testIssuer}, doc.AuthorizationServers)
	require.Contains(t, doc.ScopesSupported, ScopeMCP)
	require.Contains(t, doc.ScopesSupported, ScopeMCPRead)
	require.NotEmpty(t, doc.BearerMethods)
}

func TestAuthorizationServerMetadataWellFormed(t *testing.T) {
	t.Parallel()

	md := NewMetadata(testIssuer)
	doc := md.BuildAuthorizationServerMetadata()

	require.Equal(t, testIssuer, doc.Issuer)
	require.Equal(t, testIssuer+"/api/v1/oauth/authorize", doc.AuthorizationEndpoint)
	require.Equal(t, testIssuer+"/api/v1/oauth/token", doc.TokenEndpoint)
	require.Equal(t, testIssuer+"/api/v1/oauth/register", doc.RegistrationEndpoint)
	require.Equal(t, testIssuer+"/.well-known/jwks.json", doc.JWKSURI)

	// OAuth 2.1 hard requirements: S256-only PKCE, code grant, no implicit.
	require.Equal(t, []string{CodeChallengeMethodS256}, doc.CodeChallengeMethodsSupported)
	require.Equal(t, []string{"code"}, doc.ResponseTypesSupported)
	require.Contains(t, doc.GrantTypesSupported, GrantAuthorizationCode)
	require.Contains(t, doc.GrantTypesSupported, GrantRefreshToken)
	require.Contains(t, doc.ScopesSupported, ScopeMCP)
}

func TestProtectedResourceMetadataURL(t *testing.T) {
	t.Parallel()

	md := NewMetadata(testIssuer)
	require.Equal(t, testIssuer+"/.well-known/oauth-protected-resource", md.ProtectedResourceMetadataURL())
}

func TestBuildJWKSNeverLeaksSymmetricKey(t *testing.T) {
	t.Parallel()

	// The v1 symmetric key must never be published; the set is empty by design.
	require.Empty(t, BuildJWKS().Keys)
}

func TestTokenHasResourceAudience(t *testing.T) {
	t.Parallel()

	resource := testIssuer + "/api/v1/mcp"

	tests := []struct {
		name   string
		claims *auth.Claims
		want   bool
	}{
		{
			name:   "no audience passes (PAT/legacy back-compat)",
			claims: &auth.Claims{},
			want:   true,
		},
		{
			name: "matching audience passes",
			claims: func() *auth.Claims {
				c := &auth.Claims{}
				c.Audience = []string{resource}

				return c
			}(),
			want: true,
		},
		{
			name: "wrong audience rejected",
			claims: func() *auth.Claims {
				c := &auth.Claims{}
				c.Audience = []string{"https://other.example.com/api/v1/mcp"}

				return c
			}(),
			want: false,
		},
		{"nil claims rejected", nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, TokenHasResourceAudience(tc.claims, resource))
		})
	}
}
