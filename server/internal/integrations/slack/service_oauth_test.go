package slack

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/oauthstate"
)

func setupSlackService(t *testing.T) (context.Context, *Service) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
		Slack:  config.SlackConfig{ClientID: "test-client-id", ClientSecret: "test-client-secret"},
	}

	svc := NewService(dbService, cfg, nil, nil, nil)

	return ctx, svc
}

func TestBuildInstallURL_GeneratesValidStateAndScopes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	authorizeURL, err := svc.BuildInstallURL(ctx, "marketplace")
	r.NoError(err)

	r.True(strings.HasPrefix(authorizeURL, "https://slack.com/oauth/v2/authorize?"))
	r.Contains(authorizeURL, "client_id=test-client-id")
	r.Contains(authorizeURL, "redirect_uri=http%3A%2F%2Flocalhost%3A4000%2Fapi%2Fv1%2Fintegrations%2Fslack%2Foauth")
	r.Contains(authorizeURL, "scope=chat%3Awrite")
	r.Contains(authorizeURL, "user_scope=openid%2Cemail%2Cprofile")
	r.Contains(authorizeURL, "state=")

	// State should be redeemable as a slack-install kind, and the source
	// payload from the request should round-trip.
	stateValue := extractQueryParam(t, authorizeURL, "state")
	entry, err := oauthstate.Validate(ctx, svc.db, "slack-install", stateValue)
	r.NoError(err)
	r.Equal("marketplace", entry.Payload["source"])
}

func TestHandleOAuthCallback_RejectsMissingState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	_, err := svc.HandleOAuthCallback(ctx, "any-code", "")
	r.ErrorIs(err, ErrInvalidState)
}

func TestHandleOAuthCallback_RejectsUnknownState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	_, err := svc.HandleOAuthCallback(ctx, "any-code", "fabricated-nonce")
	r.ErrorIs(err, ErrInvalidState)
}

func TestHandleOAuthCallback_RejectsSignInState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	// A sign-in state cannot be redeemed by the install callback.
	signInNonce, err := oauthstate.Generate(ctx, svc.db, "slack-signin", nil, time.Minute)
	r.NoError(err)

	_, err = svc.HandleOAuthCallback(ctx, "any-code", signInNonce)
	r.ErrorIs(err, ErrInvalidState)
}

func TestHandleOAuthCallback_StateConsumedOnReuse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	// We can't run a full successful callback (requires mocking Slack),
	// but a successful state validation must consume the entry. The
	// callback returns a non-state error after the state passes (the
	// Slack token exchange will fail in tests), proving the state was
	// accepted, and a second call with the same state must fail with
	// ErrInvalidState.
	authorizeURL, err := svc.BuildInstallURL(ctx, "")
	r.NoError(err)

	stateValue := extractQueryParam(t, authorizeURL, "state")

	// First call: state is consumed; the call fails on the Slack token
	// exchange (no real network), surfacing as ErrOAuthFailed.
	_, firstErr := svc.HandleOAuthCallback(ctx, "fake-code", stateValue)
	r.NotNil(firstErr)
	r.NotErrorIs(firstErr, ErrInvalidState)

	// Second call with the same state: ErrInvalidState (state is gone).
	_, secondErr := svc.HandleOAuthCallback(ctx, "fake-code", stateValue)
	r.ErrorIs(secondErr, ErrInvalidState)
}

func TestIssueExchangeCode_RoundTripIsSingleUse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	result := &OAuthResult{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		OrgSlug:      "acme",
		UserUID:      "user-uid",
	}

	code, err := svc.IssueExchangeCode(ctx, result)
	r.NoError(err)
	r.NotEmpty(code)

	entry, err := oauthstate.Validate(ctx, svc.db, "slack-exchange", code)
	r.NoError(err)
	r.Equal("access-1", entry.Payload["accessToken"])
	r.Equal("refresh-1", entry.Payload["refreshToken"])
	r.Equal("acme", entry.Payload["orgSlug"])
	r.Equal("user-uid", entry.Payload["userUID"])

	// Reuse must fail.
	_, err = oauthstate.Validate(ctx, svc.db, "slack-exchange", code)
	r.ErrorIs(err, oauthstate.ErrInvalidState)
}

func extractQueryParam(t *testing.T, urlString, key string) string {
	t.Helper()

	idx := strings.Index(urlString, "?")
	require.NotEqual(t, -1, idx, "url has no query string: %s", urlString)

	for _, pair := range strings.Split(urlString[idx+1:], "&") {
		eq := strings.Index(pair, "=")
		if eq == -1 {
			continue
		}

		if pair[:eq] == key {
			return pair[eq+1:]
		}
	}

	t.Fatalf("query param %q not found in %s", key, urlString)

	return ""
}
