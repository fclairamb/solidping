package postgres

// The org-DEK cold-reload property on the REAL engine.
//
// The condition being tested is dialect-agnostic Go, but its input is not: the
// `parameters.value` column is jsonb here and TEXT-ish in SQLite, and the
// incident (#506) happened on Postgres. So the shape the reader has to cope
// with is pinned against a real Postgres round-trip, not only against SQLite.

import (
	"crypto/rand"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Distinct from every other embedded port claimed in this directory (the
// highest in use is 15510).
const portDEKColdReload = 15520

// TestDEKColdReloadPostgres encrypts with one credentials service and decrypts
// with a SECOND one on the same database — the checks worker / restarted API
// that used to fail with "unwrap org DEK: unknown envelope version" while the
// writing process kept working off its in-memory cache.
//
// Self-skips under -short and on any embedded-startup error, like its siblings.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestDEKColdReloadPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{Embedded: true, Port: portDEKColdReload, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("acme-dek", "Acme DEK")
	r.NoError(s.CreateOrganization(ctx, org))

	kek := make([]byte, 32)
	_, err = io.ReadFull(rand.Reader, kek)
	r.NoError(err)

	writer, err := credentials.NewService(kek, credentials.NewParamStore(s))
	r.NoError(err)

	envelope, err := writer.EncryptForOrg(ctx, org.UID, map[string]any{"password": "s3cr3t"})
	r.NoError(err)

	// The stored row is the wrapped scalar SetOrgParameter always writes.
	param, err := s.GetOrgParameter(ctx, org.UID, "encryption.dek")
	r.NoError(err)
	r.NotNil(param)
	r.Contains(param.Value, models.ParameterValueKey)

	// A cold second process: same KEK, same database, empty cache.
	reader, err := credentials.NewService(kek, credentials.NewParamStore(s))
	r.NoError(err)
	r.Equal(0, reader.DEKCacheLen())

	got, err := reader.DecryptForOrg(ctx, org.UID, envelope)
	r.NoError(err, "a cold process must open the org DEK on Postgres too")
	r.Equal("s3cr3t", got["password"])
}
