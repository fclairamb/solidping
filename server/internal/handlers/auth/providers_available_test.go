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

	type expected struct {
		Slack     bool
		Google    bool
		GitHub    bool
		Microsoft bool
		GitLab    bool
		Discord   bool
	}

	cases := []struct {
		name      string
		google    providerCfg
		github    providerCfg
		gitlab    providerCfg
		microsoft providerCfg
		slack     providerCfg
		discord   providerCfg
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
			}

			h := NewProvidersHandler(cfg)

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
				}
			}

			r.Equal(tc.want, got)
		})
	}
}
