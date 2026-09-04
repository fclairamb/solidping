package credentials_test

// Cold-reload coverage against REAL storage.
//
// Every pre-existing test in this package generated, encrypted and decrypted
// inside ONE service instance, so the DEK was always served from the in-memory
// cache and the stored row was never read back. That warm-cache shape is
// exactly why a DEK written as {"value": …} and read back raw could ship: the
// process that wrote it never noticed, and no test ever played the part of the
// second process. These tests do.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// newSQLiteDB returns an initialized in-memory SQLite service plus one org.
func newSQLiteDB(t *testing.T, slug string) (*sqlite.Service, *models.Organization, context.Context) {
	t.Helper()
	r := require.New(t)

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization(slug, "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return dbSvc, org, ctx
}

// TestDEKColdReloadSQLite is the regression test for incident #506: a SECOND
// service instance — standing in for a checks worker, or for the API after a
// restart — must open what the first one encrypted, having only the database
// between them.
func TestDEKColdReloadSQLite(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	dbSvc, org, ctx := newSQLiteDB(t, "acme")
	kek := newKey(t)

	// The writer: generates the org DEK and encrypts a secret.
	writer, err := credentials.NewService(kek, credentials.NewParamStore(dbSvc))
	r.NoError(err)

	envelope, err := writer.EncryptForOrg(ctx, org.UID, map[string]any{"password": "s3cr3t"})
	r.NoError(err)

	// The row really is the wrapped scalar shape — if this ever stops being
	// true, the reader's compatibility matrix needs revisiting, not silently
	// passing.
	param, err := dbSvc.GetOrgParameter(ctx, org.UID, "encryption.dek")
	r.NoError(err)
	r.NotNil(param, "the DEK must be persisted, not only cached")
	r.Contains(param.Value, models.ParameterValueKey,
		"SetOrgParameter wraps every scalar; the reader must cope with that")

	// The cold reader: a brand-new service, empty cache, same KEK, same DB.
	// This is the process that used to fail with "unwrap org DEK: unknown
	// envelope version".
	reader, err := credentials.NewService(kek, credentials.NewParamStore(dbSvc))
	r.NoError(err)
	r.Equal(0, reader.DEKCacheLen(), "the second instance must start cold")

	got, err := reader.DecryptForOrg(ctx, org.UID, envelope)
	r.NoError(err, "a cold process must be able to open the org DEK")
	r.Equal("s3cr3t", got["password"])
}

// TestDEKColdReloadSQLiteLegacyRawStringRow is the POSITIVE control for the
// legacy path: a DEK stored in the pre-wrapper shape (the envelope as a bare
// JSON string) must still open.
//
// It is asserted at the store seam rather than by writing a bare string into
// the `parameters` row, because it cannot BE a row: `parameters.value` decodes
// into a models.JSONMap, so a bare JSON string makes GetOrgParameter itself
// fail. (That is also why the hot-mitigation SQL floated in the spec —
// `set value = value->'value'` — must not be run.)
func TestDEKColdReloadSQLiteLegacyRawStringRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	dbSvc, org, ctx := newSQLiteDB(t, "globex")
	kek := newKey(t)

	writer, err := credentials.NewService(kek, credentials.NewParamStore(dbSvc))
	r.NoError(err)

	envelope, err := writer.EncryptForOrg(ctx, org.UID, map[string]any{"token": "t0ken"})
	r.NoError(err)

	// Take the DEK the writer stored and re-serve it unwrapped, exactly as a
	// pre-wrapper store would have.
	param, err := dbSvc.GetOrgParameter(ctx, org.UID, "encryption.dek")
	r.NoError(err)

	inner, ok := param.Value[models.ParameterValueKey].(string)
	r.True(ok, "the stored scalar is the envelope JSON as a string")

	legacyRow, err := json.Marshal(inner)
	r.NoError(err)

	legacyStore := credentials.ParamStore{
		Load: func(_ context.Context, _, _ string) (json.RawMessage, bool, error) {
			return legacyRow, true, nil
		},
		Save: func(_ context.Context, _, _ string, _ any, _ bool) error { return nil },
	}

	reader, err := credentials.NewService(kek, legacyStore)
	r.NoError(err)

	got, err := reader.DecryptForOrg(ctx, org.UID, envelope)
	r.NoError(err, "a DEK stored in the pre-wrapper shape must still open")
	r.Equal("t0ken", got["token"])
}

// TestDEKColdReloadSQLiteWrongMasterKey pins the other side of the taxonomy: a
// cold process holding the WRONG master key fails as an org-key problem, which
// is what stops the operator being told to re-save the check.
func TestDEKColdReloadSQLiteWrongMasterKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	dbSvc, org, ctx := newSQLiteDB(t, "initech")

	writer, err := credentials.NewService(newKey(t), credentials.NewParamStore(dbSvc))
	r.NoError(err)

	envelope, err := writer.EncryptForOrg(ctx, org.UID, map[string]any{"password": "s3cr3t"})
	r.NoError(err)

	reader, err := credentials.NewService(newKey(t), credentials.NewParamStore(dbSvc))
	r.NoError(err)

	_, err = reader.DecryptForOrg(ctx, org.UID, envelope)
	r.ErrorIs(err, credentials.ErrOrgKeyUnavailable)
	r.ErrorContains(err, "unwrap org DEK", "the documented log grep must keep working")
}
