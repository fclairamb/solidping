package auth

import (
	"errors"
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
