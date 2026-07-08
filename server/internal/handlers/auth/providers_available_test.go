package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
)

func TestListProviders(t *testing.T) {
	t.Parallel()

	type providerCfg struct {
		clientID     string
		clientSecret string
		enabled      bool
	}

	type oidcCfg struct {
		issuerURL    string
		clientID     string
		clientSecret string
		enabled      bool
	}

	type expected struct {
		Slack     bool
		Google    bool
		GitHub    bool
		Microsoft bool
		GitLab    bool
		Discord   bool
		OIDC      bool
	}

	cases := []struct {
		name      string
		google    providerCfg
		github    providerCfg
		gitlab    providerCfg
		microsoft providerCfg
		slack     providerCfg
		discord   providerCfg
		oidc      oidcCfg
		want      expected
	}{
		{
			name:   "all unset shows none",
			google: providerCfg{enabled: true},
			want:   expected{},
		},
		{
			name:   "google configured and enabled shows google only",
			google: providerCfg{clientID: "id", clientSecret: "secret", enabled: true},
			want:   expected{Google: true},
		},
		{
			name:   "google configured but disabled hides it",
			google: providerCfg{clientID: "id", clientSecret: "secret", enabled: false},
			want:   expected{},
		},
		{
			name:   "credentials missing hides even when enabled",
			google: providerCfg{clientID: "", clientSecret: "", enabled: true},
			want:   expected{},
		},
		{
			name:      "multiple providers respect their flag independently",
			google:    providerCfg{clientID: "id", clientSecret: "secret", enabled: true},
			github:    providerCfg{clientID: "id", clientSecret: "secret", enabled: false},
			gitlab:    providerCfg{clientID: "id", clientSecret: "secret", enabled: true},
			microsoft: providerCfg{clientID: "id", clientSecret: "secret", enabled: true},
			slack:     providerCfg{clientID: "id", clientSecret: "secret", enabled: false},
			discord:   providerCfg{clientID: "id", clientSecret: "secret", enabled: true},
			want:      expected{Google: true, GitLab: true, Microsoft: true, Discord: true},
		},
		{
			name: "oidc configured and enabled shows oidc",
			oidc: oidcCfg{issuerURL: "https://idp.example.com", clientID: "id", clientSecret: "secret", enabled: true},
			want: expected{OIDC: true},
		},
		{
			name: "oidc missing issuer url hides it even when enabled",
			oidc: oidcCfg{clientID: "id", clientSecret: "secret", enabled: true},
			want: expected{},
		},
		{
			name: "oidc configured but disabled hides it",
			oidc: oidcCfg{issuerURL: "https://idp.example.com", clientID: "id", clientSecret: "secret", enabled: false},
			want: expected{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			cfg := &config.Config{
				Google: config.GoogleOAuthConfig{
					Enabled:      tc.google.enabled,
					ClientID:     tc.google.clientID,
					ClientSecret: tc.google.clientSecret,
				},
				GitHub: config.GitHubOAuthConfig{
					Enabled:      tc.github.enabled,
					ClientID:     tc.github.clientID,
					ClientSecret: tc.github.clientSecret,
				},
				GitLab: config.GitLabOAuthConfig{
					Enabled:      tc.gitlab.enabled,
					ClientID:     tc.gitlab.clientID,
					ClientSecret: tc.gitlab.clientSecret,
				},
				Microsoft: config.MicrosoftOAuthConfig{
					Enabled:      tc.microsoft.enabled,
					ClientID:     tc.microsoft.clientID,
					ClientSecret: tc.microsoft.clientSecret,
				},
				Slack: config.SlackConfig{
					Enabled:      tc.slack.enabled,
					ClientID:     tc.slack.clientID,
					ClientSecret: tc.slack.clientSecret,
				},
				Discord: config.DiscordOAuthConfig{
					Enabled:      tc.discord.enabled,
					ClientID:     tc.discord.clientID,
					ClientSecret: tc.discord.clientSecret,
				},
				OIDC: config.OIDCOAuthConfig{
					Enabled:      tc.oidc.enabled,
					IssuerURL:    tc.oidc.issuerURL,
					ClientID:     tc.oidc.clientID,
					ClientSecret: tc.oidc.clientSecret,
				},
			}

			h := NewProvidersHandler(cfg, nil)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/auth/providers", nil)
			rec := httptest.NewRecorder()

			r.NoError(h.ListProviders(rec, bunrouter.Request{Request: req}))

			var resp ProvidersResponse
			r.NoError(json.NewDecoder(rec.Body).Decode(&resp))

			got := expected{}
			for _, p := range resp.Data {
				switch p.Type {
				case "slack":
					got.Slack = true
				case "google":
					got.Google = true
				case "github":
					got.GitHub = true
				case "microsoft":
					got.Microsoft = true
				case "gitlab":
					got.GitLab = true
				case "discord":
					got.Discord = true
				case "oidc":
					got.OIDC = true
				}
			}

			r.Equal(tc.want, got)
		})
	}
}
