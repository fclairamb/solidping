package auth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func paramWith(value any) *models.Parameter {
	return &models.Parameter{Value: models.JSONMap{models.ParameterValueKey: value}}
}

func TestNilClaimsHoldNoOrgRole(t *testing.T) {
	t.Parallel()

	var claims *Claims

	require.False(t, claims.HasOrgRole(models.MemberRoleViewer))
	require.True(t, (&Claims{Role: string(models.MemberRoleAdmin)}).HasOrgRole(models.MemberRoleViewer))
}

func TestNewOrganizationInfoOfNoOrganization(t *testing.T) {
	t.Parallel()

	require.Nil(t, newOrganizationInfo(nil))
	require.Equal(t, "acme", newOrganizationInfo(models.NewOrganization("acme", "Acme")).Slug)
}

func TestParseParamSeconds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		param  *models.Parameter
		want   int
		wantOK bool
	}{
		{"no parameter", nil, 0, false},
		{"JSON number", paramWith(float64(3600)), 3600, true},
		{"native int", paramWith(7200), 7200, true},
		{"string", paramWith("3600"), 0, false},
		{"no value key", &models.Parameter{Value: models.JSONMap{}}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parseParamSeconds(tt.param)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestExtractCreatedWith(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		props map[string]any
		want  *TokenCreatedWith
	}{
		{"no properties", nil, nil},
		{"no created_with entry", map[string]any{"name": "ci"}, nil},
		{"created_with of the wrong shape", map[string]any{keyCreatedWith: "password"}, nil},
		{"created_with holding nothing usable", map[string]any{keyCreatedWith: map[string]any{"method": 4}}, nil},
		{
			"full",
			map[string]any{keyCreatedWith: map[string]any{
				keyMethod: "password", "userAgent": "ua", "remoteAddr": "127.0.0.1",
			}},
			&TokenCreatedWith{Method: "password", UserAgent: "ua", RemoteAddr: "127.0.0.1"},
		},
		{
			"method only",
			map[string]any{keyCreatedWith: map[string]any{keyMethod: "passkey"}},
			&TokenCreatedWith{Method: "passkey"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, extractCreatedWith(tt.props))
		})
	}
}

func TestTokenDisplayPrefixNeverShowsMoreThanTwelveCharacters(t *testing.T) {
	t.Parallel()

	require.Equal(t, "pat_short", tokenDisplayPrefix("pat_short"))
	require.Equal(t, "pat_12345678", tokenDisplayPrefix("pat_12345678"))
	require.Equal(t, "pat_12345678", tokenDisplayPrefix("pat_1234567890abcdef"))
}

func TestScopesFromProperties(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		props models.JSONMap
		want  []string
	}{
		{"no scopes", models.JSONMap{}, nil},
		{"native strings", models.JSONMap{keyScopes: []string{"mcp"}}, []string{"mcp"}},
		{"decoded JSON", models.JSONMap{keyScopes: []any{"mcp", "mcp:read"}}, []string{"mcp", "mcp:read"}},
		{"decoded JSON with a stray value", models.JSONMap{keyScopes: []any{"mcp", 12}}, []string{"mcp"}},
		{"unknown shape", models.JSONMap{keyScopes: "mcp"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, scopesFromProperties(tt.props))
		})
	}
}

func TestMaskEmail(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":               "",
		"not-an-address": "not-an-address",
		"a@acme.com":     "a***@acme.com",
		"al@acme.com":    "a***@acme.com",
		"alice@acme.com": "al***@acme.com",
	}
	for input, want := range tests {
		require.Equal(t, want, maskEmail(input), "input %q", input)
	}
}

func TestEmbedOriginsFromParamIsNeverNil(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{}, EmbedOriginsFromParam(nil))
	require.Equal(t, []string{}, EmbedOriginsFromParam(paramWith(42)))
	require.Equal(t, []string{"https://status.acme.com"},
		EmbedOriginsFromParam(paramWith("https://status.acme.com")))
}

func TestParamBoolDefaultTrue(t *testing.T) {
	t.Parallel()

	require.True(t, paramBoolDefaultTrue(nil))
	require.True(t, paramBoolDefaultTrue(paramWith("false")), "a value of the wrong type is the default")
	require.True(t, paramBoolDefaultTrue(paramWith(true)))
	require.False(t, paramBoolDefaultTrue(paramWith(false)))
}

func TestOrgSummariesSkipMembershipsWithoutOrganization(t *testing.T) {
	t.Parallel()

	org := models.NewOrganization("acme", "Acme")
	members := []*models.OrganizationMember{
		{OrganizationUID: "gone", Role: models.MemberRoleAdmin},
		{OrganizationUID: org.UID, Role: models.MemberRoleViewer, Organization: org},
	}

	require.Equal(t,
		[]OrganizationSummary{{Slug: "acme", Name: "Acme", Role: string(models.MemberRoleViewer)}},
		buildOrgSummaries(members))
	require.Equal(t, string(models.MemberRoleViewer), findRoleInMembers(members, org.UID))
	require.Empty(t, findRoleInMembers(members, "another-org"))
}

func TestProviderLabels(t *testing.T) {
	t.Parallel()

	named := &Service{fullCfg: &config.Config{
		SAML: config.SAMLConfig{DisplayName: "Acme SSO"},
		OIDC: config.OIDCOAuthConfig{DisplayName: "Acme ID"},
	}}
	plain := &Service{fullCfg: &config.Config{}}

	require.Equal(t, "Acme SSO", named.providerLabel(models.ProviderTypeSAML))
	require.Equal(t, "Acme ID", named.providerLabel(models.ProviderTypeOIDC))
	require.Equal(t, ssoFallbackLabel, plain.providerLabel(models.ProviderTypeSAML))
	require.Equal(t, ssoFallbackLabel, plain.providerLabel(models.ProviderTypeOIDC))
	require.Equal(t, "Slack", plain.providerLabel(models.ProviderTypeSlack))
	require.Equal(t, ssoFallbackLabel, plain.providerLabel(models.ProviderType("acme-idp")))

	// SAML and OIDC share the fallback label: it is listed once, and the
	// result is sorted whatever the order of the links.
	require.Equal(t, []string{"Google", ssoFallbackLabel, "Slack"}, plain.providerLabels([]*models.UserProvider{
		{ProviderType: models.ProviderTypeSlack},
		{ProviderType: models.ProviderTypeSAML},
		{ProviderType: models.ProviderTypeOIDC},
		{ProviderType: models.ProviderTypeGoogle},
	}))
}
