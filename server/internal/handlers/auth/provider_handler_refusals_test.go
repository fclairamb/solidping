package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/oauthstate"
)

type httpHandlerFunc func(http.ResponseWriter, *http.Request) error

// providerRoutes is the login and callback route of one redirect-based
// provider, on its own database.
type providerRoutes struct {
	name     string
	db       db.Service
	login    httpHandlerFunc
	callback httpHandlerFunc
	// orgScoped: the login is started from an organization's login page.
	orgScoped bool
}

func redirectProviderRoutes(t *testing.T) []providerRoutes {
	t.Helper()

	github, ctx := setupGitHubTestService(t)
	google, _ := setupGoogleTestService(t)
	gitlab, _ := setupGitLabTestService(t)
	microsoft, _ := setupMicrosoftTestService(t)
	discord := newDiscordTestService(t, newSQLiteDBService(t))
	slack, _ := setupSlackTestService(t)

	setupGitHubTestOrg(ctx, t, github)
	setupTestOrg(ctx, t, google)
	setupGitLabTestOrg(ctx, t, gitlab)
	setupMicrosoftTestOrg(ctx, t, microsoft)

	githubH := NewGitHubOAuthHandler(github, github.cfg)
	googleH := NewGoogleOAuthHandler(google, google.cfg)
	gitlabH := NewGitLabOAuthHandler(gitlab, gitlab.cfg)
	microsoftH := NewMicrosoftOAuthHandler(microsoft, microsoft.cfg)
	discordH := NewDiscordOAuthHandler(discord, discord.cfg)
	slackH := NewSlackOAuthHandler(slack, slack.cfg)

	return []providerRoutes{
		{"github", github.db, githubH.Login, githubH.Callback, true},
		{"google", google.db, googleH.Login, googleH.Callback, true},
		{"gitlab", gitlab.db, gitlabH.Login, gitlabH.Callback, true},
		{"microsoft", microsoft.db, microsoftH.Login, microsoftH.Callback, true},
		{"discord", discord.db, discordH.Login, discordH.Callback, false},
		{"slack", slack.db, slackH.Login, slackH.Callback, false},
	}
}

// serve runs a provider route and returns the response.
func serveRoute(t *testing.T, handler httpHandlerFunc, target string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	require.NoError(t, handler(rec, req))

	return rec
}

// errorRedirect returns where an error redirect points and its error fields.
func errorRedirect(t *testing.T, rec *httptest.ResponseRecorder) (string, string, string) {
	t.Helper()

	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())

	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)

	return location.Path, location.Query().Get("error"), location.Query().Get("error_description")
}

// TestProviderCallbackRefusesUnusableRequests: a callback the provider
// reports as denied, that lacks its parameters, or whose state is unknown is
// sent back to the dashboard with an error. It never reaches the provider.
func TestProviderCallbackRefusesUnusableRequests(t *testing.T) { //nolint:tparallel // one service per provider
	t.Parallel()

	tests := []struct {
		name     string
		query    string
		wantCode string
	}{
		{"denied at the provider", "error=access_denied", "access_denied"},
		{"no code", "state=abc", "INVALID_REQUEST"},
		{"no state", "code=abc", "INVALID_REQUEST"},
		{"unknown state", "code=abc&state=no-such-state", OAuthCodeInvalidState},
	}

	for _, routes := range redirectProviderRoutes(t) { //nolint:paralleltest // subtests share one fixture and run in order
		for _, tt := range tests {
			t.Run(routes.name+"/"+tt.name, func(t *testing.T) {
				rec := serveRoute(t, routes.callback, "/api/v1/auth/"+routes.name+"/callback?"+tt.query)

				path, code, description := errorRedirect(t, rec)
				require.Equal(t, "/", path)
				require.Equal(t, tt.wantCode, code)
				require.NotEmpty(t, description)
				require.NotContains(t, rec.Header().Get("Location"), "code=abc", "nothing of the request is echoed")
			})
		}
	}
}

// TestProviderLoginRefusals: a login for an organization that does not exist
// goes back to the login page, and a state that cannot be stored is a 500
// instead of a redirect to the provider with a state nobody will recognize.
func TestProviderLoginRefusals(t *testing.T) { //nolint:tparallel // subtests share one service per provider
	t.Parallel()

	for _, routes := range redirectProviderRoutes(t) { //nolint:paralleltest // subtests share one fixture and run in order
		target := "/api/v1/auth/" + routes.name + "/login"
		if routes.orgScoped {
			target += "?org=test-org"

			t.Run(routes.name+"/unknown organization", func(t *testing.T) {
				rec := serveRoute(t, routes.login, "/api/v1/auth/"+routes.name+"/login?org=no-such-org")

				path, code, _ := errorRedirect(t, rec)
				require.True(t, strings.HasSuffix(path, "/login"), path)
				require.Equal(t, string(base.ErrorCodeOrganizationNotFound), code)
			})
		}

		t.Run(routes.name+"/state cannot be stored", func(t *testing.T) {
			fault := installQueryFault(routes.db)

			var rec *httptest.ResponseRecorder

			fault.during(t, failingSQL(`"state_entries"`), func() { rec = serveRoute(t, routes.login, target) })
			require.Equal(t, http.StatusInternalServerError, rec.Code)
			require.Equal(t, string(base.ErrorCodeInternalError), errorBody(t, rec)["code"])
			require.Empty(t, rec.Header().Get("Location"))

			// Control: with a healthy store the same request goes to the provider.
			rec = serveRoute(t, routes.login, target)
			require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
			require.Contains(t, rec.Header().Get("Location"), "state=")
		})
	}
}

// TestSlackExchangeEndpoint covers POST /auth/slack/exchange.
func TestSlackExchangeEndpoint(t *testing.T) {
	t.Parallel()

	svc, ctx := setupSlackTestService(t)
	handler := NewSlackOAuthHandler(svc, svc.cfg)

	post := func(t *testing.T, body string) *httptest.ResponseRecorder {
		t.Helper()

		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/slack/exchange", strings.NewReader(body))
		rec := httptest.NewRecorder()
		require.NoError(t, handler.Exchange(rec, req))

		return rec
	}

	refusals := []struct {
		name     string
		body     string
		wantCode base.ErrorCode
	}{
		{"body that is not JSON", "{", base.ErrorCodeValidationError},
		{"no code", `{}`, base.ErrorCodeValidationError},
		{"unknown code", `{"code":"no-such-code"}`, base.ErrorCodeInvalidToken},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := post(t, tt.body)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, string(tt.wantCode), errorBody(t, rec)["code"])
		})
	}

	t.Run("a minted code redeems once", func(t *testing.T) {
		t.Parallel()

		code, err := oauthstate.Generate(ctx, svc.db, "slack-exchange", map[string]any{
			"accessToken": "access", "refreshToken": "refresh", "orgSlug": "acme", "userUID": "user-1", "expiresIn": 900,
		}, time.Minute)
		require.NoError(t, err)

		rec := post(t, `{"code":"`+code+`"}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		body := errorBody(t, rec)
		require.Equal(t, "access", body["accessToken"])
		require.Equal(t, "refresh", body["refreshToken"])
		require.Equal(t, "acme", body["orgSlug"])
		require.InDelta(t, 900, body["expiresIn"], 0)

		rec = post(t, `{"code":"`+code+`"}`)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, string(base.ErrorCodeInvalidToken), errorBody(t, rec)["code"])
	})
}
