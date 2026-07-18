package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
)

// portTunnelPG is distinct from every other embedded-Postgres port claimed in
// the repo (5435, 15434, 15436-15448, 15451-15453) so the suites can never
// collide if they ever run concurrently.
const portTunnelPG = 15454

// TestListChecksByTunnelCheckUID_Postgres exercises the dependent lookup on a
// REAL Postgres backend. The delete guard is the one place where the two
// backends genuinely diverge — `config->>'tunnelCheckUid'` on Postgres JSONB
// versus `json_extract(config, '$.tunnelCheckUid')` on SQLite — so the SQLite
// suite alone could never catch a broken JSONB predicate here.
//
// Self-skips under `-short` (the default `make test` / CI mode) and on any
// embedded-startup error, mirroring the other embedded-postgres tests.
func TestListChecksByTunnelCheckUID_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portTunnelPG,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("tunnel-pg-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	otherOrg := models.NewOrganization("tunnel-pg-other-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, otherOrg))

	bastion := models.NewCheck(org.UID, "pg-bastion", "ssh")
	bastion.Config = models.JSONMap{"host": "bastion.example.com", "expected_fingerprint": testFingerprint}
	r.NoError(dbSvc.CreateCheck(ctx, bastion))

	dependent := models.NewCheck(org.UID, "pg-dependent", "http")
	dependent.Config = models.JSONMap{
		"url":                              "http://internal.private/",
		checkerdef.TunnelCheckUIDConfigKey: bastion.UID,
	}
	r.NoError(dbSvc.CreateCheck(ctx, dependent))

	// An untunneled check in the same org must not match.
	unrelated := models.NewCheck(org.UID, "pg-unrelated", "http")
	unrelated.Config = models.JSONMap{"url": "https://example.com"}
	r.NoError(dbSvc.CreateCheck(ctx, unrelated))

	// A check in ANOTHER org referencing the same bastion uid must not match:
	// the query is org-scoped, so one tenant's delete guard can never be
	// tripped (or informed) by another tenant's rows.
	foreign := models.NewCheck(otherOrg.UID, "pg-foreign", "http")
	foreign.Config = models.JSONMap{
		"url":                              "http://internal.private/",
		checkerdef.TunnelCheckUIDConfigKey: bastion.UID,
	}
	r.NoError(dbSvc.CreateCheck(ctx, foreign))

	found, err := dbSvc.ListChecksByTunnelCheckUID(ctx, org.UID, bastion.UID)
	r.NoError(err)
	r.Len(found, 1)
	r.Equal(dependent.UID, found[0].UID)

	// A soft-deleted dependent stops blocking.
	r.NoError(dbSvc.DeleteCheck(ctx, dependent.UID))

	found, err = dbSvc.ListChecksByTunnelCheckUID(ctx, org.UID, bastion.UID)
	r.NoError(err)
	r.Empty(found)
}
