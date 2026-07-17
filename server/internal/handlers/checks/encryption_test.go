package checks_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
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

	svc, dbSvc, _, org := setupEncryptedChecksServiceCreds(t)

	return svc, dbSvc, org
}

// setupEncryptedChecksServiceCreds is setupEncryptedChecksService plus the
// credentials service, for tests that need to read (or plant) an encrypted
// envelope directly.
func setupEncryptedChecksServiceCreds(
	t *testing.T,
) (*checks.Service, db.Service, credentials.Service, *models.Organization) {
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

	return svc, dbSvc, creds, org
}

// privateKeys reads a check row's advertised encrypted key names.
func privateKeys(t *testing.T, row *models.Check) []string {
	t.Helper()

	if row.ConfigPrivateKeys == nil {
		return nil
	}

	var keys []string
	require.NoError(t, json.Unmarshal([]byte(*row.ConfigPrivateKeys), &keys))

	return keys
}

// decryptPrivate decrypts a check row's private envelope.
func decryptPrivate(t *testing.T, creds credentials.Service, row *models.Check) map[string]any {
	t.Helper()

	require.NotNil(t, row.ConfigPrivate)

	private, err := creds.DecryptForOrg(t.Context(), row.OrganizationUID, *row.ConfigPrivate)
	require.NoError(t, err)

	return private
}

// TestUpdateCheckPreservesEncryptedSecret is the headline test from the
// credentials-encryption spec: PATCHing a check without re-sending the
// credential MUST preserve the encrypted value from the existing row, never
// silently wipe it.
//
// Both halves of an HTTP basic-auth credential now live in one reserved,
// encrypted `basicAuth` key — `username` no longer sits in the public column.
func TestUpdateCheckPreservesEncryptedSecret(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, creds, org := setupEncryptedChecksServiceCreds(t)
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
	r.NotContains(created.Config, "username", "the username half must be encrypted too")
	r.NotContains(created.Config, "basicAuth", "the folded credential must not appear in the public response")
	r.Equal([]string{"basicAuth"}, created.ConfigPrivateKeys)

	// Read the row directly to confirm the credential really lives in the
	// envelope, not in the public column.
	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotContains(row.Config, "password")
	r.NotContains(row.Config, "username")
	r.NotContains(row.Config, "basicAuth")
	r.NotNil(row.ConfigPrivate)
	r.NotEmpty(*row.ConfigPrivate)

	// PATCH without re-sending the credential — the headline scenario. This is
	// what the dashboard sends when the user edits anything but the auth
	// section (the credential never comes back on GET, so it can't be echoed).
	newName := "renamed-but-still-secret"
	configWithoutSecret := map[string]any{
		"url": "https://example.com/health",
	}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
		Name:   &newName,
		Config: &configWithoutSecret,
	})
	r.NoError(err)

	// The encrypted credential must still be there — intact, both halves.
	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(after.ConfigPrivate, "PATCH dropped the encrypted secret — regression of the spec's headline rule")
	r.Equal([]string{"basicAuth"}, privateKeys(t, after))
	r.Equal("alice:hunter2", decryptPrivate(t, creds, after)["basicAuth"])
	r.Equal("https://example.com/health", after.Config["url"])
}

// TestCreateCheckFoldsBasicAuthOverExistingValue covers the idempotency rule:
// re-sending a username/password pair overwrites any previously folded
// credential rather than merging into it.
func TestUpdateCheckReplacesFoldedBasicAuth(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, creds, org := setupEncryptedChecksServiceCreds(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "rotating-secret",
		Slug: "rotating-secret",
		Type: "http",
		Config: map[string]any{
			"url":      "https://example.com",
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.NoError(err)

	rotated := map[string]any{
		"url":      "https://example.com",
		"username": "bob",
		"password": "s3cret",
	}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &rotated})
	r.NoError(err)

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal("bob:s3cret", decryptPrivate(t, creds, after)["basicAuth"])
}

// TestUpdateCheckFoldsLegacyRowOnUnrelatedPatch is the key migration
// regression: a row written before the fold carries plaintext
// `username`/`password`; an unrelated PATCH must fold it into `basicAuth`
// without losing the credential (the password half is preserved by the
// secret-merge, the username half round-trips through the public config).
func TestUpdateCheckFoldsLegacyRowOnUnrelatedPatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, creds, org := setupEncryptedChecksServiceCreds(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:   "legacy-http",
		Slug:   "legacy-http",
		Type:   "http",
		Config: map[string]any{"url": "https://example.com"},
	})
	r.NoError(err)

	// Rewrite the row into the pre-fold shape, bypassing the service: public
	// `username`, encrypted `password`, no `basicAuth`.
	legacyPublic := models.JSONMap{"url": "https://example.com", "username": "alice"}
	envelope, err := creds.EncryptForOrg(ctx, org.UID, map[string]any{"password": "hunter2"})
	r.NoError(err)
	legacyKeys := `["password"]`
	r.NoError(dbSvc.UpdateCheck(ctx, created.UID, &models.CheckUpdate{
		Config:            &legacyPublic,
		ConfigPrivate:     &envelope,
		ConfigPrivateKeys: &legacyKeys,
	}))

	// An unrelated PATCH: the dashboard round-trips the public `username` it
	// was given and never sees the password.
	patched := map[string]any{"url": "https://example.com/health", "username": "alice"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patched})
	r.NoError(err)

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotContains(after.Config, "username", "the legacy username must be folded away from the public config")
	r.NotContains(after.Config, "password")
	r.Equal([]string{"basicAuth"}, privateKeys(t, after))
	r.Equal("alice:hunter2", decryptPrivate(t, creds, after)["basicAuth"],
		"the legacy credential was lost on the fold")
}

// TestUpdateCheckPreservesSecretHeadersOnUnrelatedPatch covers the wipe bug:
// secret headers are never returned by GET, so a PATCH that doesn't mention
// them must keep them.
func TestUpdateCheckPreservesSecretHeadersOnUnrelatedPatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, creds, org := setupEncryptedChecksServiceCreds(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "headers-http",
		Slug: "headers-http",
		Type: "http",
		Config: map[string]any{
			"url":           "https://example.com",
			"secretHeaders": map[string]any{"X-Api-Key": "topsecret"},
		},
	})
	r.NoError(err)
	r.Equal([]string{"secretHeaders"}, created.ConfigPrivateKeys)

	onlyURL := map[string]any{"url": "https://example.com/health"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &onlyURL})
	r.NoError(err)

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal([]string{"secretHeaders"}, privateKeys(t, after))

	headers, ok := decryptPrivate(t, creds, after)["secretHeaders"].(map[string]any)
	r.True(ok)
	r.Equal("topsecret", headers["X-Api-Key"])
}

// TestUpdateCheckClearsSecretWhenSentEmpty verifies the inverse: if the
// caller does send the reserved key with an empty value, the encrypted
// credential is cleared.
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
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.NoError(err)
	r.Equal([]string{"basicAuth"}, created.ConfigPrivateKeys)

	// What the form sends when both auth inputs are touched and emptied.
	configClearing := map[string]any{
		"url":       "https://example.com",
		"basicAuth": nil,
	}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
		Config: &configClearing,
	})
	r.NoError(err)

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotContains(privateKeys(t, after), "basicAuth")
	r.NotContains(after.Config, "basicAuth")
	r.NotContains(after.Config, "password")
	r.NotContains(after.Config, "username")
}

// TestUpdateCheckRejectsColonInUsername pins the RFC 7617 rule. UpdateCheck
// never calls checker.Validate, so normalization is the only validation the
// PATCH path has — its error must be a config error (→ HTTP 400), not a 500.
func TestUpdateCheckRejectsColonInUsername(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:   "colon-http",
		Slug:   "colon-http",
		Type:   "http",
		Config: map[string]any{"url": "https://example.com"},
	})
	r.NoError(err)

	bad := map[string]any{"url": "https://example.com", "username": "al:ice", "password": "x"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &bad})
	r.Error(err)

	configErr := checkerdef.IsConfigError(err)
	r.NotNil(configErr, "a bad username must surface as a 400-mapped config error")
	r.Equal("username", configErr.Parameter)
}

// TestCreateCheckDropsPasswordWithoutUsername pins the execution-gate mirror:
// a password-only config sends no auth header, so it stores no credential and
// its stray password must not linger anywhere.
func TestCreateCheckDropsPasswordWithoutUsername(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "password-only",
		Slug: "password-only",
		Type: "http",
		Config: map[string]any{
			"url":      "https://example.com",
			"password": "hunter2",
		},
	})
	r.NoError(err)
	r.Empty(created.ConfigPrivateKeys)

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotContains(after.Config, "password")
	r.NotContains(after.Config, "basicAuth")
	r.Nil(after.ConfigPrivate)
}
