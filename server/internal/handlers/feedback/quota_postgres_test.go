package feedback

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portFeedbackQuotaPG: distinct from every other _postgres_test.go port
// claimed in the repo (see the port-numbering comment in
// postgres_headroom_postgres_test.go / orgparams' service_postgres_test.go).
const portFeedbackQuotaPG = 15561

// ONE embedded Postgres for the whole package, booted once and shared —
// mirrors internal/handlers/orgparams' service_postgres_test.go, which
// documents why (concurrent embedded-postgres instances race over a shared
// pwfile).
//
//nolint:gochecknoglobals // one process-wide fixture, torn down in TestMain
var (
	feedbackPGOnce sync.Once
	feedbackPGSvc  *postgres.Service
	errFeedbackPG  error
)

// TestMain closes the shared instance after the last test in this package.
func TestMain(m *testing.M) {
	code := m.Run()

	if feedbackPGSvc != nil {
		_ = feedbackPGSvc.Close()
	}

	os.Exit(code)
}

// newFeedbackPostgresOrg returns the shared embedded Postgres plus a fresh
// organization, self-skipping under -short and deferring to
// testsupport.PostgresUnavailable when embedded Postgres genuinely cannot
// start (a skip locally, a hard failure under SP_TEST_REQUIRE_POSTGRES=1 — see
// wiki/testing/test-layers.md).
func newFeedbackPostgresOrg(t *testing.T, slug string) (*postgres.Service, *models.Organization) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	feedbackPGOnce.Do(func() {
		ctx := context.Background()

		svc, err := postgres.New(ctx, &postgres.Config{
			Embedded: true, Port: portFeedbackQuotaPG, RunMode: "test",
		})
		if err != nil {
			errFeedbackPG = err

			return
		}

		if initErr := svc.Initialize(ctx); initErr != nil {
			errFeedbackPG = initErr
			_ = svc.Close()

			return
		}

		feedbackPGSvc = svc
	})

	if errFeedbackPG != nil || feedbackPGSvc == nil {
		testsupport.PostgresUnavailable(t, errFeedbackPG)
	}

	r := require.New(t)
	org := models.NewOrganization(slug, "Feedback Quota PG")
	r.NoError(feedbackPGSvc.CreateOrganization(t.Context(), org))

	return feedbackPGSvc, org
}

// TestSubmitReport_StorageQuota_Postgres is the Postgres half of the pair; see
// quota_test.go's TestSubmitReport_StorageQuota_SQLite for the SQLite half.
// Both run the IDENTICAL assertions, so the pair proves parity rather than
// two tests that happen to agree.
func TestSubmitReport_StorageQuota_Postgres(t *testing.T) {
	t.Parallel()

	dbSvc, org := newFeedbackPostgresOrg(t, "fb-quota-pg")
	assertStorageQuotaEnforced(t.Context(), t, dbSvc, org.UID, org.Slug)
}
