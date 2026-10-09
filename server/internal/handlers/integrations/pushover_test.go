package integrations_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
)

func newPushoverTestSvc(t *testing.T) (*integrations.Service, *sqlite.Service, *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("pushover-org", "Pushover Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return integrations.NewService(dbSvc, creds, nil, &config.Config{}), dbSvc, org
}

// TestPushover_DashboardKeysAreEncrypted asserts the keys the dashboard form
// writes (user_key / api_token) land in the encrypted settings_private, not
// in the public settings JSON (spec 2026-10-08-03).
func TestPushover_DashboardKeysAreEncrypted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newPushoverTestSvc(t)

	created, err := svc.CreateIntegration(t.Context(), org.Slug, integrations.CreateIntegrationRequest{
		Type:     "pushover",
		Name:     "phone",
		Settings: map[string]any{"user_key": "u-secret", "api_token": "a-secret"},
	})
	r.NoError(err)

	row, err := dbSvc.GetChannel(t.Context(), created.UID)
	r.NoError(err)
	r.NotContains(row.Settings, "user_key")
	r.NotContains(row.Settings, "api_token")
	r.NotNil(row.SettingsPrivate)
	r.NotContains(*row.SettingsPrivate, "a-secret")
	r.NotNil(row.SettingsPrivateKeys)

	var keys []string
	r.NoError(json.Unmarshal([]byte(*row.SettingsPrivateKeys), &keys))
	r.ElementsMatch([]string{"api_token", "user_key"}, keys)
}

// TestPushover_TestReportsMissingToken asserts a Pushover integration with no
// API token is reported as a configuration error with its own code, not as a
// generic delivery failure.
func TestPushover_TestReportsMissingToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _, org := newPushoverTestSvc(t)

	created, err := svc.CreateIntegration(t.Context(), org.Slug, integrations.CreateIntegrationRequest{
		Type:     "pushover",
		Name:     "phone",
		Settings: map[string]any{"user_key": "u-secret"},
	})
	r.NoError(err)

	result, err := svc.TestIntegration(t.Context(), org.Slug, created.UID)
	r.NoError(err)
	r.False(result.Success)
	r.Equal(integrations.TestResultCodeMisconfigured, result.Code)
	r.Equal("API token", result.MissingSetting)
	r.NotEmpty(result.Error)
}
