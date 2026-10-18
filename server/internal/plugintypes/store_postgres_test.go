package plugintypes_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
	"github.com/fclairamb/solidping/server/internal/plugintypes"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// Distinct from every other _postgres_test.go embedded port in the repo.
const portPluginTypes = 15791

// TestReport_Postgres runs the upsert and the org scoping on PostgreSQL (the
// jsonb column, the uuid key and the check constraint of migration 029).
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestReport_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portPluginTypes, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	orgA := models.NewOrganization("plugin-types-a", "Plugin Types A")
	r.NoError(svc.CreateOrganization(ctx, orgA))
	orgB := models.NewOrganization("plugin-types-b", "Plugin Types B")
	r.NoError(svc.CreateOrganization(ctx, orgB))

	store := plugintypes.New(svc.DB())

	for _, description := range []string{"first", "second"} {
		stored, reportErr := store.Report(ctx, orgA.UID, []protocol.TypeManifest{reported("x-acme", description)})
		r.NoError(reportErr)
		r.Equal(1, stored)
	}

	list, err := store.List(ctx, orgA.UID)
	r.NoError(err)
	r.Len(list, 1)
	r.Equal("second", list[0].Meta().Description)

	_, ok, err := store.Lookup(ctx, orgB.UID, "x-acme")
	r.NoError(err)
	r.False(ok)
}
