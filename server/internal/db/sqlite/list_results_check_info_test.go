package sqlite

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// insertOrphanResult inserts one raw result with the given UID referencing a
// non-existent check, on a connection with foreign keys switched off — the
// single-row, explicit-UID sibling of insertOrphanResults (fk_orphan_test.go)
// so callers can identify the seeded row afterwards.
func insertOrphanResult(t *testing.T, svc *Service, orgUID, checkUID, resultUID string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	conn, err := svc.db.Conn(ctx)
	r.NoError(err)
	defer func() { _ = conn.Close() }()

	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF")
	r.NoError(err)

	_, err = conn.ExecContext(ctx,
		"INSERT INTO results (uid, organization_uid, check_uid, period_type, period_start, status) "+
			"VALUES (?, ?, ?, 'raw', '2026-07-01 00:00:00', 3)",
		resultUID, orgUID, checkUID,
	)
	r.NoError(err)

	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	r.NoError(err)
}

// TestListResults_IncludeCheckInfo pins the SQLite side of spec
// 2026-08-18-07: ListResultsFilter.IncludeCheckInfo joins `checks` and
// populates the scan-only CheckSlug/CheckName columns on models.Result. This
// test must fail against the pre-fix code (applyResultsFilter never read
// IncludeCheckInfo, so both fields stayed nil regardless of the flag).
func TestListResults_IncludeCheckInfo(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = s.Close() })
	r.NoError(s.Initialize(ctx))

	org := models.NewOrganization("check-info-org", "Check Info Org")
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
}

// TestListResults_IncludeCheckInfo_OrphanedCheck pins the LEFT JOIN half of
// spec 2026-08-18-07: a result whose check_uid no longer resolves to a row in
// `checks` (the FK-orphan scenario covered by RequireCheckExists) must still
// come back on the page — an INNER JOIN would silently drop it, which is a
// worse bug than the one being fixed.
func TestListResults_IncludeCheckInfo_OrphanedCheck(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = s.Close() })
	r.NoError(s.Initialize(ctx))

	org := models.NewOrganization("check-info-orphan", "Check Info Orphan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	orphanCheckUID := uuid.NewString()
	orphanResultUID := uuid.NewString()
	insertOrphanResult(t, s, org.UID, orphanCheckUID, orphanResultUID)

	resp, err := s.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID:  org.UID,
		PeriodTypes:      []string{models.PeriodTypeRaw},
		Limit:            10,
		IncludeCheckInfo: true,
	})
	r.NoError(err)

	var orphanRow *models.Result
	for _, row := range resp.Results {
		if row.UID == orphanResultUID {
			orphanRow = row
		}
	}
	r.NotNil(orphanRow, "an orphaned result must still be returned, not dropped by the join")
	r.Nil(orphanRow.CheckSlug, "an orphaned result has no check to read a slug from")
	r.Nil(orphanRow.CheckName, "an orphaned result has no check to read a name from")
}
