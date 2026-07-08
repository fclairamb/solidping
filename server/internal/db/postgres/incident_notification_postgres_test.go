package postgres

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portOrgNotification is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portOrgNotification = 15447

// TestGetOrgNotification_MalformedUID_Postgres is the Postgres counterpart to
// TestGetByOrgNotFound (handler_test.go, which runs against SQLite): uid is a
// uuid-typed column, so a non-UUID identifier like "does-not-exist" used to
// error on the implicit cast (invalid input syntax for type uuid) instead of
// matching zero rows — surfacing as a 500 instead of the friendly not-found
// state (caught by web/dash0/e2e/notification-detail.spec.ts against a real
// Postgres backend in CI, not reproducible against the SQLite-backed
// handler unit test). Self-skips under -short / on embedded-startup error,
// like every other embedded-postgres test in this package.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestGetOrgNotification_MalformedUID_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     portOrgNotification,
		RunMode:  runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("org-notif-pg-org", "Org Notification PG Org")
	r.NoError(s.CreateOrganization(ctx, org))

	_, err = s.GetOrgNotification(ctx, org.UID, "does-not-exist")
	r.ErrorIs(err, sql.ErrNoRows)

	// A well-formed but nonexistent UUID must still behave the same way.
	_, err = s.GetOrgNotification(ctx, org.UID, "00000000-0000-0000-0000-000000000000")
	r.ErrorIs(err, sql.ErrNoRows)
}
