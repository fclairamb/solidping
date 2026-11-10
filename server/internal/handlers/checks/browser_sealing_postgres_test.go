package checks_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portBrowserSealingPG is the embedded Postgres of the browser-sealing suite,
// distinct from every other _postgres_test.go port in the repo.
const portBrowserSealingPG = 15742

// TestBrowserSealing_Postgres runs the browser-sealed secrets scenarios of
// browser_sealing_test.go on a real Postgres (spec 2026-11-08-01, Tests): the
// sealed blob, the private key list and the region-key parameter rows go
// through the Postgres dialect too. One embedded server, a fresh org per case.
func TestBrowserSealing_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portBrowserSealingPG, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	var orgSeq atomic.Int64

	env := func(t *testing.T) (*checks.Service, db.Service, *models.Organization) {
		t.Helper()

		creds, credErr := credentials.NewService(newKEK(t), newMemDEKStore())
		require.NoError(t, credErr)

		slug := fmt.Sprintf("seal-pg-%d", orgSeq.Add(1))
		org := models.NewOrganization(slug, "Browser Sealing PG "+slug)
		require.NoError(t, dbSvc.CreateOrganization(t.Context(), org))

		return checks.NewService(dbSvc, nil, creds, nil), dbSvc, org
	}

	cases := map[string]func(*testing.T, sealingEnv){
		"create stores client blob":   testBrowserSealedCreateStoresClientBlob,
		"create refusals":             testBrowserSealedCreateRefusals,
		"stale fingerprints":          testBrowserSealedStaleFingerprintsCarryCurrentRecipients,
		"mode switches":               testBrowserSealedModeSwitches,
		"required mode":               testBrowserSealingRequiredMode,
		"patch replacement rules":     testBrowserSealedPatchReplacementRules,
		"server-sealed partial patch": testServerSealedOnlyPartialPatchRefused,
		"recipients endpoint":         testSealingRecipientsEndpoint,
		"shared region key sealing":   testSharedRegionKeySealing,
		"bulk region change refused":  testBulkRegionChangeSkipsBrowserSealed,
		"reseal checks list":          testResealChecksList,
	}

	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, env)
		})
	}
}
