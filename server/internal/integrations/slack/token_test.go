package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// These tests pin spec 2026-09-18-02: the bot token lives in the encrypted
// `settings_private` envelope, so every reader that used to parse the public
// settings map saw an empty token and reported an installed app as missing.

// splitSlackConn stores a Slack integration whose token is ONLY in a private
// plaintext envelope — the exact shape every row has after the startup split.
func splitSlackConn(
	ctx context.Context, t *testing.T, svc *Service, orgUID, token string,
) *models.Integration {
	t.Helper()

	envelope, err := credentials.SealPlaintext(map[string]any{"access_token": token})
	require.NoError(t, err)

	conn := models.NewIntegration(orgUID, models.ConnectionTypeSlack, "workspace")
	conn.Enabled = true
	conn.IsDefault = true
	conn.Settings = models.JSONMap{"team_id": "T1", "team_name": "Workspace"}
	conn.SettingsPrivate = &envelope
	require.NoError(t, svc.db.CreateChannel(ctx, conn))

	return conn
}

func TestBotToken_ReadsThePrivateEnvelope(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	envelope, err := credentials.SealPlaintext(map[string]any{"access_token": "xoxb-private"})
	r.NoError(err)

	conn := &models.Integration{
		UID:             "conn-1",
		OrganizationUID: "org-1",
		Type:            models.ConnectionTypeSlack,
		Settings:        models.JSONMap{"team_id": "T1"},
		SettingsPrivate: &envelope,
	}

	token, err := BotToken(t.Context(), nil, conn)
	r.NoError(err)
	r.Equal("xoxb-private", token)
}

func TestBotToken_TokenlessStubIsNotConnected(t *testing.T) {
	t.Parallel()

	conn := &models.Integration{
		UID:      "conn-1",
		Type:     models.ConnectionTypeSlack,
		Settings: models.JSONMap{"team_id": "T1"},
	}

	_, err := BotToken(t.Context(), nil, conn)
	require.ErrorIs(t, err, ErrSlackNotConnected)
}

// TestGetClient_SplitRow proves inbound events, slash commands and thread
// replies authenticate again: GetClient used to hand newAPIClient("").
func TestGetClient_SplitRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("slack-client-org", "Slack Client Org")
	r.NoError(svc.db.CreateOrganization(ctx, org))
	splitSlackConn(ctx, t, svc, org.UID, "xoxb-client")

	var seen string

	svc.newAPIClient = func(token string) *Client {
		seen = token

		return NewClient(token)
	}

	client, err := svc.GetClient(ctx, "T1")
	r.NoError(err)
	r.NotNil(client)
	r.Equal("xoxb-client", seen)
}

// TestGetDestinations_SplitRow proves the dashboard channel picker stops
// answering 409 CHANNEL_NOT_CONNECTED for an app that is installed.
func TestGetDestinations_SplitRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if req.URL.Path == "/users.list" {
			_, _ = w.Write([]byte(`{"ok":true,"members":[{"id":"U1","name":"alice","real_name":"Alice"}]}`))

			return
		}

		_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C1","name":"alerts"}]}`))
	}))
	t.Cleanup(fake.Close)

	org := models.NewOrganization("slack-dest-org", "Slack Dest Org")
	r.NoError(svc.db.CreateOrganization(ctx, org))
	conn := splitSlackConn(ctx, t, svc, org.UID, "xoxb-destinations")

	svc.newAPIClient = func(token string) *Client {
		r.Equal("xoxb-destinations", token)

		return NewClientWithBaseURL(token, fake.URL)
	}

	resp, err := svc.GetDestinations(ctx, org.Slug, conn.UID)
	r.NoError(err)
	r.Len(resp.Channels, 1)
	r.Len(resp.Users, 1)
}

// TestGetDestinations_TokenlessStubStillRefuses is the negative control: a
// manually-created stub with no token anywhere must still be reported as not
// connected, not sent to Slack with an empty bearer.
func TestGetDestinations_TokenlessStubStillRefuses(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("slack-stub-org", "Slack Stub Org")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "stub")
	conn.Settings = models.JSONMap{"team_id": "T9"}
	r.NoError(svc.db.CreateChannel(ctx, conn))

	_, err := svc.GetDestinations(ctx, org.Slug, conn.UID)
	r.ErrorIs(err, ErrSlackNotConnected)
}

// TestCreateOrUpdateConnection_SealsTheToken is the storage-shape guard the
// spec asks for: the install path must never leave access_token in the public
// settings column, and must declare it in settings_private_keys.
func TestCreateOrUpdateConnection_SealsTheToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("slack-install-org", "Slack Install Org")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	resp := &OAuthResponse{
		AccessToken: "xoxb-installed",
		BotUserID:   "U0BOT",
		Scope:       "chat:write,channels:read",
	}
	resp.Team.ID = "T42"
	resp.Team.Name = "Install Org"
	resp.AuthedUser.ID = "U0ADMIN"

	connUID, err := svc.createOrUpdateConnection(ctx, org.UID, resp)
	r.NoError(err)

	conn, err := svc.db.GetChannel(ctx, connUID)
	r.NoError(err)

	r.NotContains(conn.Settings, "access_token", "the public settings column must never carry the bot token")
	r.Equal("T42", conn.Settings["team_id"])
	r.NotNil(conn.SettingsPrivate)
	r.NotNil(conn.SettingsPrivateKeys)

	var keys []string
	r.NoError(json.Unmarshal([]byte(*conn.SettingsPrivateKeys), &keys))
	r.Equal([]string{"access_token"}, keys)

	// And the token is readable again through the helper every reader uses.
	token, err := BotToken(ctx, svc.creds, conn)
	r.NoError(err)
	r.Equal("xoxb-installed", token)

	// A re-install of the same workspace must keep the same shape.
	resp.AccessToken = "xoxb-reinstalled"
	_, err = svc.createOrUpdateConnection(ctx, org.UID, resp)
	r.NoError(err)

	conn, err = svc.db.GetChannel(ctx, connUID)
	r.NoError(err)
	r.NotContains(conn.Settings, "access_token")

	token, err = BotToken(ctx, svc.creds, conn)
	r.NoError(err)
	r.Equal("xoxb-reinstalled", token)
}
