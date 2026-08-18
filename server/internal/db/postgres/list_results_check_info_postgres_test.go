package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portCheckInfo is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portCheckInfo = 15474

// TestListResults_IncludeCheckInfo_Postgres is the Postgres counterpart to
// the SQLite check-info test for spec 2026-08-18-07: ListResultsFilter.
// IncludeCheckInfo joins `checks` and populates the scan-only
// CheckSlug/CheckName columns on models.Result. This test must fail against
// the pre-fix code (applyResultsFilter never read IncludeCheckInfo, so both
// fields stayed nil regardless of the flag).
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction)
func TestListResults_IncludeCheckInfo_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{Embedded: true, Port: portCheckInfo, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("check-info-pg-org", "Check Info PG Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "acme-http", "http")
	name := "acme HTTP"
	check.Name = &name
	r.NoError(s.CreateCheck(ctx, check))

	seeded := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
	seeded.PeriodStart = time.Now().Add(time.Hour)
	r.NoError(s.CreateResult(ctx, seeded))

	findSeeded := func(results []*models.Result) *models.Result {
		for _, row := range results {
			if row.UID == seeded.UID {
				return row
			}
		}

		r.FailNow("seeded result not returned by ListResults")

		return nil
	}

	// Positive: IncludeCheckInfo joins checks and populates both fields with
	// the check's actual slug/name.
	withInfo, err := s.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID:  org.UID,
		CheckUIDs:        []string{check.UID},
		PeriodTypes:      []string{models.PeriodTypeRaw},
		Limit:            10,
		IncludeCheckInfo: true,
	})
	r.NoError(err)
	row := findSeeded(withInfo.Results)
	r.NotNil(row.CheckSlug, "check_slug must be populated when IncludeCheckInfo is set")
	r.Equal("acme-http", *row.CheckSlug)
	r.NotNil(row.CheckName, "check_name must be populated when IncludeCheckInfo is set")
	r.Equal(name, *row.CheckName)

	// Negative control: without IncludeCheckInfo, no join happens and both
	// fields stay nil — proves the positive case above isn't vacuous.
	withoutInfo, err := s.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		PeriodTypes:     []string{models.PeriodTypeRaw},
		Limit:           10,
	})
	r.NoError(err)
	rowNoInfo := findSeeded(withoutInfo.Results)
	r.Nil(rowNoInfo.CheckSlug, "check_slug must stay nil without IncludeCheckInfo")
	r.Nil(rowNoInfo.CheckName, "check_name must stay nil without IncludeCheckInfo")

	// A result whose check has since been hard-deleted (FK-orphan, seeded the
	// way the production orphans arose — see seedOrphanResultPG) must still
	// come back on the page: an INNER JOIN would silently drop it, a worse bug
	// than the one being fixed.
	orphanCheckUID := uuid.Must(uuid.NewV7()).String()
	orphanResultUID := uuid.Must(uuid.NewV7()).String()
	seedOrphanResultPG(t, s, org.UID, orphanCheckUID, orphanResultUID, seeded.PeriodStart.Add(time.Minute))

	withOrphan, err := s.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID:  org.UID,
		PeriodTypes:      []string{models.PeriodTypeRaw},
		Limit:            10,
		IncludeCheckInfo: true,
	})
	r.NoError(err)

	var orphanRow *models.Result
	for _, resultRow := range withOrphan.Results {
		if resultRow.UID == orphanResultUID {
			orphanRow = resultRow
		}
	}
	r.NotNil(orphanRow, "an orphaned result must still be returned, not dropped by the join")
	r.Nil(orphanRow.CheckSlug, "an orphaned result has no check to read a slug from")
	r.Nil(orphanRow.CheckName, "an orphaned result has no check to read a name from")
}
