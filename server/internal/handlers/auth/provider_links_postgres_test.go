package auth

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
)

// portProviderLinksPG is distinct from every other embedded-Postgres port
// claimed in the repo (see the port-numbering note in
// internal/db/incident_number_test.go).
const portProviderLinksPG = 15501

// TestProviderLinkHealing_Postgres is the real-engine twin of the SQLite
// contracts in discord_service_test.go / slack_service_test.go.
//
// It is not ceremony: the heal turns on Postgres-specific schema behavior that
// SQLite exercises differently. Clearing an organization_providers row is a SOFT
// delete, and the re-link only becomes insertable because
// `idx_org_providers_type_id` is PARTIAL (`where deleted_at is null`); the
// user_providers row must be HARD deleted because
// `user_providers_provider_idx` is not. A fix that passed on SQLite alone could
// still deadlock on the real unique indexes in production.
//
//nolint:paralleltest,tparallel // one embedded PG instance shared by every sub-test
func TestProviderLinkHealing_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	dbService := newPostgresDBService(t)

	t.Run("discord stale guild link", func(t *testing.T) {
		testDiscordStaleGuildLinkHeals(t, dbService)
	})

	t.Run("discord stale personal-org link", func(t *testing.T) {
		testDiscordStalePersonalOrgLinkHeals(t, dbService)
	})

	t.Run("discord stale user link", func(t *testing.T) {
		testDiscordStaleUserLinkHeals(t, dbService)
	})

	t.Run("discord live link is left alone", func(t *testing.T) {
		testDiscordLiveLinkIsReused(t, dbService)
	})

	t.Run("slack stale team link", func(t *testing.T) {
		testSlackStaleTeamLinkHeals(t, dbService)
	})

	t.Run("slack stale user link", func(t *testing.T) {
		testSlackStaleUserLinkHeals(t, dbService)
	})
}

// newPostgresDBService boots one embedded Postgres shared by every sub-test
// above (booting one per case would blow past the embedded server's connection
// budget and multiply an already slow start-up). The sub-tests therefore run
// sequentially and each uses its own provider ids / org slugs so they cannot
// collide on the unique indexes.
func newPostgresDBService(t *testing.T) db.Service {
	t.Helper()

	ctx := t.Context()

	dbService, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portProviderLinksPG,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbService.Close() })

	if initErr := dbService.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	return dbService
}
