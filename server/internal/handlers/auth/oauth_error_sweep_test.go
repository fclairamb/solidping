package auth

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

var errAcmeExploded = errors.New("acme exploded")

// TestEveryProviderMapsEveryOAuthError feeds every sentinel error to every
// provider's error redirect: each must answer with a non-empty error code and
// description, never an empty redirect.
func TestEveryProviderMapsEveryOAuthError(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	handlers := map[string]func(http.ResponseWriter, *http.Request, string, error) error{
		"discord":   NewDiscordOAuthHandler(nil, cfg).handleOAuthError,
		"github":    NewGitHubOAuthHandler(nil, cfg).handleOAuthError,
		"google":    NewGoogleOAuthHandler(nil, cfg).handleOAuthError,
		"gitlab":    NewGitLabOAuthHandler(nil, cfg).handleOAuthError,
		"microsoft": NewMicrosoftOAuthHandler(nil, cfg).handleOAuthError,
		"oidc":      NewOIDCOAuthHandler(nil, cfg).handleOAuthError,
		"slack":     NewSlackOAuthHandler(nil, cfg).handleOAuthError,
		"saml":      NewSAMLHandler(nil, cfg).handleSAMLError,
	}
	sentinels := []error{
		ErrDiscordTokenExchange, ErrEmailNotVerified, ErrGitHubTokenExchange, ErrGitLabTokenExchange,
		ErrGitLabLinkRequiresSignIn,
		ErrGoogleTokenExchange, ErrInvalidOAuthState, ErrMicrosoftTokenExchange, ErrOIDCNotConfigured,
		ErrOIDCTokenExchange, ErrOIDCTokenInvalid, ErrSAMLAssertionInvalid, ErrSAMLNoEmail,
		ErrSAMLNotConfigured, ErrSlackTokenExchange, errAcmeExploded,
	}

	for name, handle := range handlers {
		for _, sentinel := range sentinels {
			params := redirectParams(t, func(w http.ResponseWriter, req *http.Request) error {
				return handle(w, req, "/d/orgs/default", sentinel)
			})
			require.NotEmpty(t, params.Get("error"), "%s: %v", name, sentinel)
			require.NotEmpty(t, params.Get("error_description"), "%s: %v", name, sentinel)
		}
	}
}

// TestTokenExchangeFailureIsLogged: the user only sees "Failed to exchange
// authorization code", so the provider's own answer (code already used,
// expired, redirect_uri mismatch) must reach the server log, or a failed
// sign-in cannot be diagnosed afterwards.
//
//nolint:paralleltest // Swaps the process-global slog default logger.
func TestTokenExchangeFailureIsLogged(t *testing.T) {
	cfg := &config.Config{}
	cases := []struct {
		provider string
		handle   func(http.ResponseWriter, *http.Request, string, error) error
		sentinel error
	}{
		{"discord", NewDiscordOAuthHandler(nil, cfg).handleOAuthError, ErrDiscordTokenExchange},
		{"github", NewGitHubOAuthHandler(nil, cfg).handleOAuthError, ErrGitHubTokenExchange},
		{"gitlab", NewGitLabOAuthHandler(nil, cfg).handleOAuthError, ErrGitLabTokenExchange},
		{"google", NewGoogleOAuthHandler(nil, cfg).handleOAuthError, ErrGoogleTokenExchange},
		{"microsoft", NewMicrosoftOAuthHandler(nil, cfg).handleOAuthError, ErrMicrosoftTokenExchange},
		{"oidc", NewOIDCOAuthHandler(nil, cfg).handleOAuthError, ErrOIDCTokenExchange},
		{"slack", NewSlackOAuthHandler(nil, cfg).handleOAuthError, ErrSlackTokenExchange},
	}

	for _, tc := range cases {
		capture := captureDefaultLogs(t)
		cause := fmt.Errorf("%w: invalid_grant: Bad Request", tc.sentinel)

		params := redirectParams(t, func(w http.ResponseWriter, req *http.Request) error {
			return tc.handle(w, req, "/d/orgs/acme", cause)
		})
		require.Equal(t, OAuthCodeTokenExchange, params.Get("error"), tc.provider)
		require.Equal(t, OAuthDescTokenExchange, params.Get("error_description"), tc.provider)

		logs := capture.String()
		require.Contains(t, logs, "OAuth token exchange failed", tc.provider)
		require.Contains(t, logs, "provider="+tc.provider, tc.provider)
		require.Contains(t, logs, "invalid_grant", "%s: the provider's answer must be logged", tc.provider)
	}
}
