package credmigrate_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/credmigrate"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// memDEKStore is an in-memory DEKStore for the reconcile tests.
type memDEKStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemDEKStore() *memDEKStore { return &memDEKStore{data: map[string][]byte{}} }

func (s *memDEKStore) LoadDEK(_ context.Context, orgUID string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[orgUID]
	if !ok {
		return nil, false, nil
	}

	return v, true, nil
}

func (s *memDEKStore) SaveDEK(_ context.Context, orgUID string, wrapped []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[orgUID] = wrapped

	return nil
}

func newKEK(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)

	return key
}

// seedEncryptedConnection inserts an integration whose given private keys are
// encrypted under the org DEK, mirroring what applySettingsEncryption would
// have written before the registry fix.
func seedEncryptedConnection(
	ctx context.Context, t *testing.T, dbSvc *sqlite.Service, creds credentials.Service,
	org *models.Organization, connType models.ConnectionType,
	public, private map[string]any,
) *models.Integration {
	t.Helper()
	r := require.New(t)

	conn := models.NewIntegration(org.UID, connType, "seed")
	conn.Settings = models.JSONMap(public)

	envelope, err := creds.EncryptForOrg(ctx, org.UID, private)
	r.NoError(err)
	conn.SettingsPrivate = &envelope

	keysJSON, err := json.Marshal(credentials.SortedKeys(private))
	r.NoError(err)
	keysStr := string(keysJSON)
	conn.SettingsPrivateKeys = &keysStr

	r.NoError(dbSvc.CreateChannel(ctx, conn))

	return conn
}

func TestReconcileConnectionRegistry_DemotesWebhookURL(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("reconcile-webhook", "Reconcile Webhook Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Seed a webhook with url + signingSecret + auth_token all encrypted, as
	// the old registry would have stored them.
	conn := seedEncryptedConnection(ctx, t, dbSvc, creds, org, models.ConnectionTypeWebhook,
		map[string]any{},
		map[string]any{
			"url":           "https://example.com/hook",
			"signingSecret": "whsec_abc",
			"auth_token":    "tok_123",
		})

	stats, err := credmigrate.ReconcileConnectionRegistry(ctx, dbSvc, creds, credmigrate.Options{})
	r.NoError(err)
	r.Equal(1, stats.ConnectionsReconciled)

	got, err := dbSvc.GetChannel(ctx, conn.UID)
	r.NoError(err)

	// url moved to public settings...
	r.Equal("https://example.com/hook", got.Settings["url"])

	// ...and is gone from settings_private_keys.
	r.NotNil(got.SettingsPrivateKeys)
	var keys []string
	r.NoError(json.Unmarshal([]byte(*got.SettingsPrivateKeys), &keys))
	r.NotContains(keys, "url")
	r.Contains(keys, "signingSecret")
	r.Contains(keys, "auth_token")

	// Remaining secrets stay encrypted and still decrypt to original values.
	r.NotNil(got.SettingsPrivate)
	r.NotEmpty(*got.SettingsPrivate)
	dec, err := creds.DecryptForOrg(ctx, org.UID, *got.SettingsPrivate)
	r.NoError(err)
	r.Equal("whsec_abc", dec["signingSecret"])
	r.Equal("tok_123", dec["auth_token"])
	r.NotContains(dec, "url")
}

func TestReconcileConnectionRegistry_NullsPrivateWhenOnlyURL(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("reconcile-discord", "Reconcile Discord Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Discord stored only webhook_url (its sole former secret) encrypted.
	conn := seedEncryptedConnection(ctx, t, dbSvc, creds, org, models.ConnectionTypeDiscord,
		map[string]any{},
		map[string]any{"webhook_url": "https://discord.example/hook"})

	stats, err := credmigrate.ReconcileConnectionRegistry(ctx, dbSvc, creds, credmigrate.Options{})
	r.NoError(err)
	r.Equal(1, stats.ConnectionsReconciled)

	got, err := dbSvc.GetChannel(ctx, conn.UID)
	r.NoError(err)

	r.Equal("https://discord.example/hook", got.Settings["webhook_url"])
	// No secrets remain → private columns nulled.
	r.Nil(got.SettingsPrivate)
	r.Nil(got.SettingsPrivateKeys)
}

func TestReconcileConnectionRegistry_Idempotent(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("reconcile-idem", "Reconcile Idempotent Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	conn := seedEncryptedConnection(ctx, t, dbSvc, creds, org, models.ConnectionTypeWebhook,
		map[string]any{},
		map[string]any{
			"url":           "https://example.com/hook",
			"signingSecret": "whsec_abc",
		})

	// First run reconciles.
	stats1, err := credmigrate.ReconcileConnectionRegistry(ctx, dbSvc, creds, credmigrate.Options{})
	r.NoError(err)
	r.Equal(1, stats1.ConnectionsReconciled)

	first, err := dbSvc.GetChannel(ctx, conn.UID)
	r.NoError(err)

	// Second run is a no-op.
	stats2, err := credmigrate.ReconcileConnectionRegistry(ctx, dbSvc, creds, credmigrate.Options{})
	r.NoError(err)
	r.Equal(0, stats2.ConnectionsReconciled)
	r.Equal(1, stats2.ConnectionsScanned)

	second, err := dbSvc.GetChannel(ctx, conn.UID)
	r.NoError(err)
	r.Equal("https://example.com/hook", second.Settings["url"])
	r.Equal(*first.SettingsPrivateKeys, *second.SettingsPrivateKeys)
}

func TestReconcileConnectionRegistry_NoOpWhenEncryptionDisabled(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	disabled, err := credentials.NewService(nil, newMemDEKStore())
	r.NoError(err)
	r.False(disabled.Enabled())

	stats, err := credmigrate.ReconcileConnectionRegistry(ctx, dbSvc, disabled, credmigrate.Options{})
	r.NoError(err)
	r.Equal(0, stats.ConnectionsScanned)
	r.Equal(0, stats.ConnectionsReconciled)
}
