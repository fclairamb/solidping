package integrations_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
)

const (
	ntfyTestServerURL = "https://ntfy.sh"
	ntfyTestTopic     = "solidping-alerts"
)

// TestNtfyLegacyAccessTokenRedactedOnRead covers a row written before the
// legacy accessToken key was declared secret: the token sits in the public
// settings column. Any member reading the integration must not get it back:
// it is redacted and advertised in settingsPrivateKeys, while the non-secret
// server_url and topic stay visible (positive control).
func TestNtfyLegacyAccessTokenRedactedOnRead(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, _, org := setupEncryptedIntegrationsService(t)
	ctx := t.Context()

	conn := models.NewIntegration(org.UID, models.ConnectionTypeNtfy, "Legacy ntfy")
	conn.Settings = models.JSONMap{
		"server_url":  ntfyTestServerURL,
		"topic":       ntfyTestTopic,
		"accessToken": "tk_legacy_secret",
	}
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	got, err := svc.GetIntegration(ctx, org.Slug, conn.UID)
	r.NoError(err)
	r.NotContains(got.Settings, "accessToken", "the legacy ntfy token must never be in an API response")
	r.Contains(got.SettingsPrivateKeys, "accessToken")
	r.Equal(ntfyTestServerURL, got.Settings["server_url"])
	r.Equal(ntfyTestTopic, got.Settings["topic"])
}

// TestNtfyCreateWithLegacyAccessToken covers a client that still writes the
// legacy key: the token is stored encrypted under the canonical auth_token
// key, never returned, and the public column holds neither key.
func TestNtfyCreateWithLegacyAccessToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, creds, org := setupEncryptedIntegrationsService(t)
	ctx := t.Context()

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type: "ntfy",
		Name: "Ops ntfy",
		Settings: map[string]any{
			"server_url":  ntfyTestServerURL,
			"topic":       ntfyTestTopic,
			"accessToken": "tk_created",
		},
	})
	r.NoError(err)
	r.NotContains(created.Settings, "accessToken")
	r.NotContains(created.Settings, "auth_token")
	r.Contains(created.SettingsPrivateKeys, "auth_token", "a legacy accessToken is renamed to auth_token")
	r.NotContains(created.SettingsPrivateKeys, "accessToken")
	r.Equal(ntfyTestTopic, created.Settings["topic"])

	got, err := svc.GetIntegration(ctx, org.Slug, created.UID)
	r.NoError(err)
	r.NotContains(got.Settings, "accessToken")
	r.NotContains(got.Settings, "auth_token")
	r.Equal(ntfyTestServerURL, got.Settings["server_url"])

	row, err := dbSvc.GetChannel(ctx, created.UID)
	r.NoError(err)
	r.NotContains(row.Settings, "accessToken")
	r.NotContains(row.Settings, "auth_token")
	r.NotNil(row.SettingsPrivate)
	private, err := creds.DecryptForOrg(ctx, org.UID, *row.SettingsPrivate)
	r.NoError(err)
	r.Equal("tk_created", private["auth_token"])
	r.NotContains(private, "accessToken")
}

// TestNtfyPatchWithLegacyAccessToken covers a PATCH that supplies the legacy
// key: it replaces the stored token under auth_token.
func TestNtfyPatchWithLegacyAccessToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, creds, org := setupEncryptedIntegrationsService(t)
	ctx := t.Context()

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type: "ntfy",
		Name: "Ops ntfy",
		Settings: map[string]any{
			"server_url": ntfyTestServerURL,
			"topic":      ntfyTestTopic,
			"auth_token": "tk_old",
		},
	})
	r.NoError(err)

	updated, err := svc.UpdateIntegration(ctx, org.Slug, created.UID, integrations.UpdateIntegrationRequest{
		Settings: map[string]any{
			"server_url":  ntfyTestServerURL,
			"topic":       ntfyTestTopic,
			"accessToken": "tk_rotated",
		},
	})
	r.NoError(err)
	r.NotContains(updated.Settings, "accessToken")
	r.Equal(ntfyTestTopic, updated.Settings["topic"])

	row, err := dbSvc.GetChannel(ctx, created.UID)
	r.NoError(err)
	r.NotContains(row.Settings, "accessToken")
	r.NotNil(row.SettingsPrivate)
	private, err := creds.DecryptForOrg(ctx, org.UID, *row.SettingsPrivate)
	r.NoError(err)
	r.Equal("tk_rotated", private["auth_token"])
	r.NotContains(private, "accessToken")
}
