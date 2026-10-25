package discord

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestOAuthCallbackRedirects drives the install callback through each of its
// outcomes: provider error, missing parameters, a rejected state, and a
// completed install landing on the new integration.
func TestOAuthCallbackRedirects(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, _ := setupDiscordService(t)
	org := models.NewOrganization("acme", "acme")
	r.NoError(svc.db.CreateOrganization(ctx, org))
	h := NewHandler(svc, svc.cfg)

	call := func(query string) string {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/integrations/discord/oauth?"+query, nil)
		rec := httptest.NewRecorder()
		r.NoError(h.OAuthCallback(rec, req))
		r.Equal(http.StatusFound, rec.Code, query)

		return rec.Header().Get("Location")
	}

	r.Contains(call("error=access_denied"), "oauth_failed")
	r.Contains(call("code=x"), "state_invalid")
	r.Contains(call("code=x&state=forged"), "state_invalid")

	state := installState(ctx, t, svc, org.Slug)
	location := call("code=fake-code&state=" + state)
	r.True(strings.HasPrefix(location, "http://localhost:4000/d/orgs/acme/integrations/"), location)

	// The installed guild is counted once.
	count, err := svc.CountInstalledGuilds(ctx)
	r.NoError(err)
	r.Equal(1, count)
}

func TestInstallErrorReason(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for err, want := range map[error]string{
		ErrInvalidState:          "state_invalid",
		ErrGuildMissing:          "guild_missing",
		ErrOAuthFailed:           "oauth_failed",
		errors.New("acme broke"): "unknown",
	} {
		require.Equal(t, want, installErrorReason(req, err))
	}
}
