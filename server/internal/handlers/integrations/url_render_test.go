package integrations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
)

// newURLTestSvc builds an integrations service over an in-memory SQLite DB,
// with encryption either enabled or disabled, plus an isolated org.
func newURLTestSvc(t *testing.T, encryptionEnabled bool) (*integrations.Service, *models.Organization, context.Context) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	var kek []byte
	if encryptionEnabled {
		kek = newKEK(t)
	}

	creds, err := credentials.NewService(kek, newMemDEKStore())
	r.NoError(err)
	r.Equal(encryptionEnabled, creds.Enabled())

	slug := "url" + sanitizeSlug(t.Name())
	org := models.NewOrganization(slug, "URL Render Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return integrations.NewService(dbSvc, creds, nil, nil), org, ctx
}

// TestGetIntegration_WebhookURLRendered asserts the webhook `url` is surfaced
// on GET (so the edit form populates) under both encryption modes, while the
// signing secret is still surfaced and auth_token stays masked.
func TestGetIntegration_WebhookURLRendered(t *testing.T) {
	t.Parallel()

	for _, enc := range []bool{false, true} {
		enc := enc
		name := "encryptionOff"
		if enc {
			name = "encryptionOn"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			svc, org, ctx := newURLTestSvc(t, enc)

			created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
				Type: "webhook",
				Name: "hook",
				Settings: map[string]any{
					"url":        "https://example.com/hook",
					"auth_token": "tok_secret",
				},
			})
			r.NoError(err)

			got, err := svc.GetIntegration(ctx, org.Slug, created.UID)
			r.NoError(err)

			// URL renders on the edit form.
			r.Equal("https://example.com/hook", got.Settings["url"])

			// Signing secret is intentionally retrievable.
			secret, ok := got.Settings["signingSecret"].(string)
			r.True(ok)
			r.NotEmpty(secret)
			r.NotContains(got.SettingsPrivateKeys, "signingSecret")

			// auth_token stays masked: never in plaintext settings, only a
			// placeholder pill when encryption is on.
			r.NotContains(got.Settings, "auth_token")
			if enc {
				r.Contains(got.SettingsPrivateKeys, "auth_token")
			}
		})
	}
}

// TestGetIntegration_WebhookURLFieldRendered asserts the `webhook_url` field of
// discord / googlechat / mattermost renders on GET under both encryption modes.
func TestGetIntegration_WebhookURLFieldRendered(t *testing.T) {
	t.Parallel()

	types := []string{"discord", "googlechat", "mattermost"}

	for _, enc := range []bool{false, true} {
		for _, connType := range types {
			enc := enc
			connType := connType
			name := connType + "-encryptionOff"
			if enc {
				name = connType + "-encryptionOn"
			}

			t.Run(name, func(t *testing.T) {
				t.Parallel()
				r := require.New(t)
				svc, org, ctx := newURLTestSvc(t, enc)

				created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
					Type: connType,
					Name: connType,
					Settings: map[string]any{
						"webhook_url": "https://" + connType + ".example/hook",
					},
				})
				r.NoError(err)

				got, err := svc.GetIntegration(ctx, org.Slug, created.UID)
				r.NoError(err)

				r.Equal("https://"+connType+".example/hook", got.Settings["webhook_url"],
					"%s webhook_url must render on the edit form", connType)
				// webhook_url is no longer a secret → never a private key.
				r.NotContains(got.SettingsPrivateKeys, "webhook_url")
			})
		}
	}
}
