package checks_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// memDEKStore satisfies credentials.DEKStore for tests without a DB.
type memDEKStore struct {
	data map[string][]byte
}

func newMemDEKStore() *memDEKStore { return &memDEKStore{data: map[string][]byte{}} }

func (s *memDEKStore) LoadDEK(_ context.Context, orgUID string) ([]byte, bool, error) {
	v, ok := s.data[orgUID]
	if !ok {
		return nil, false, nil
	}

	return v, true, nil
}

func (s *memDEKStore) SaveDEK(_ context.Context, orgUID string, wrapped []byte) error {
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

func setupEncryptedChecksService(t *testing.T) (*checks.Service, db.Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	require.NoError(t, err)

	org := models.NewOrganization("encrypt-test", "Encryption Test Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	svc := checks.NewService(dbSvc, nil, creds, nil)

	return svc, dbSvc, org
}

// TestUpdateCheckPreservesEncryptedSecret is the headline test from the
// credentials-encryption spec: PATCHing a check without re-sending the
// password key MUST preserve the encrypted password from the existing
// row, never silently wipe it.
func TestUpdateCheckPreservesEncryptedSecret(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "secret-http",
		Slug: "secret-http",
		Type: "http",
		Config: map[string]any{
			"url":      "https://example.com",
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.NoError(err)
	r.NotContains(created.Config, "password", "password must not appear in the public response")
	r.Contains(created.ConfigPrivateKeys, "password")

	// Read the row directly to confirm the password really lives in the
	// envelope, not in the public column.
	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotContains(row.Config, "password")
	r.NotNil(row.ConfigPrivate)
	r.NotEmpty(*row.ConfigPrivate)

	// PATCH without re-sending the password — the headline scenario.
	newName := "renamed-but-still-secret"
	configWithoutSecret := map[string]any{
		"url":      "https://example.com",
		"username": "alice-renamed",
	}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
		Name:   &newName,
		Config: &configWithoutSecret,
	})
	r.NoError(err)

	// The encrypted password must still be there.
	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(after.ConfigPrivate, "PATCH dropped the encrypted secret — regression of the spec's headline rule")
	r.NotEmpty(*after.ConfigPrivate)

	var keys []string
	r.NoError(json.Unmarshal([]byte(*after.ConfigPrivateKeys), &keys))
	r.Contains(keys, "password")

	// Username on the public side should reflect the patch.
	r.Equal("alice-renamed", after.Config["username"])
}

// TestUpdateCheckClearsSecretWhenSentEmpty verifies the inverse: if the
// caller does send the secret key with an empty value, the encrypted
// payload is cleared.
func TestUpdateCheckClearsSecretWhenSentEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "clear-secret",
		Slug: "clear-secret",
		Type: "http",
		Config: map[string]any{
			"url":      "https://example.com",
			"password": "hunter2",
		},
	})
	r.NoError(err)

	configClearing := map[string]any{
		"url":      "https://example.com",
		"password": "",
	}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
		Config: &configClearing,
	})
	r.NoError(err)

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)

	if after.ConfigPrivate != nil {
		// Allowed to be NULL or to be an envelope of an empty private map;
		// the key list must not advertise password as encrypted.
		var keys []string
		r.NoError(json.Unmarshal([]byte(*after.ConfigPrivateKeys), &keys))
		r.NotContains(keys, "password")
	}

	r.NotContains(after.Config, "password")
}
