package checks_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// setupLastStatusChangeService builds a checks service over an in-memory
// SQLite database, plus an organization to hang fixtures off.
func setupLastStatusChangeService(t *testing.T) (*checks.Service, db.Service, *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("lsc-org", "Last Status Change Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	return svc, dbSvc, org
}

// clearRawResults drops every result row of a check, so a fixture can control
// the raw history exactly — CreateCheck writes an initial "created" marker row
// that would otherwise be a genuine transition inside the retention window.
func clearRawResults(ctx context.Context, t *testing.T, dbSvc db.Service, checkUID string) {
	t.Helper()
	r := require.New(t)

	sqliteSvc, ok := dbSvc.(*sqlite.Service)
	r.True(ok, "fixture needs the SQLite backend")

	_, err := sqliteSvc.DB().NewRaw("DELETE FROM results WHERE check_uid = ?", checkUID).Exec(ctx)
	r.NoError(err)
}

// seedUniformRawResults writes n raw rows for a check, all carrying the SAME
// status, one hour apart ending at `newest`. This is the shape retention
// leaves behind for a check that has been stable for a long time: the older
// rows (including the genuine transition) have been rolled up into hour/day
// buckets and deleted, so the oldest SURVIVING raw row is not a transition at
// all. Returns the oldest surviving row's timestamp — the value the old LAG
// query used to report as "last status change".
func seedUniformRawResults(
	ctx context.Context, t *testing.T, dbSvc db.Service,
	orgUID, checkUID string, status models.ResultStatus, n int, newest time.Time,
) time.Time {
	t.Helper()
	r := require.New(t)

	oldest := newest.Add(-time.Duration(n-1) * time.Hour)
	for i := 0; i < n; i++ {
		result := models.NewResult(orgUID, checkUID, status, 12)
		result.PeriodStart = oldest.Add(time.Duration(i) * time.Hour)
		r.NoError(dbSvc.CreateResult(ctx, result))
	}

	return oldest
}

// legacyLagStatusChange runs the LAG-over-`results` query this spec deleted,
// verbatim, against the test database. It exists only as a positive control:
// a fixture that no longer trips the old query would make the regression
// assertions below meaningless.
func legacyLagStatusChange(
	ctx context.Context, t *testing.T, dbSvc db.Service, checkUID string,
) time.Time {
	t.Helper()
	r := require.New(t)

	sqliteSvc, ok := dbSvc.(*sqlite.Service)
	r.True(ok, "control query needs the SQLite backend")

	var row struct {
		PeriodStart time.Time `bun:"period_start"`
	}

	const query = `
		WITH status_changes AS (
			SELECT check_uid, period_start, status,
				LAG(status) OVER (PARTITION BY check_uid ORDER BY period_start) AS prev_status
			FROM results
			WHERE check_uid = ? AND period_type = 'raw' AND status IS NOT NULL
		)
		SELECT period_start
		FROM status_changes
		WHERE status != prev_status OR prev_status IS NULL
		ORDER BY period_start DESC
		LIMIT 1
	`

	r.NoError(sqliteSvc.DB().NewRaw(query, checkUID).Scan(ctx, &row))

	return row.PeriodStart
}

// TestLastStatusChange_IsNotTheRetentionHorizon is the regression test for the
// bug in spec 2026-08-09-07: the former LAG-based query treated the first row
// of each check's partition as a transition (its LAG is NULL, and
// "status IS DISTINCT FROM NULL" is true), so once older raw rows aged out it
// reported the raw-retention horizon — a timestamp that slid forward on every
// compaction run — instead of the real transition time.
//
// Fixture: every surviving raw row shares one status, and the check's real
// transition (checks.status_changed_at) is far older than all of them.
func TestLastStatusChange_IsNotTheRetentionHorizon(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	svc, dbSvc, org := setupLastStatusChangeService(t)

	check := models.NewCheck(org.UID, "stable-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))
	clearRawResults(ctx, t, dbSvc, check.UID)

	// Four surviving raw rows, all "up" — nothing in them is a transition,
	// exactly what retention leaves behind for a long-stable check.
	newest := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	oldestSurviving := seedUniformRawResults(
		ctx, t, dbSvc, org.UID, check.UID, models.ResultStatusUp, 4, newest,
	)

	// The real transition: this check went up 34 days ago and stayed up.
	realChange := time.Now().Add(-34 * 24 * time.Hour).UTC().Truncate(time.Second)
	r.NoError(dbSvc.UpdateCheckStatusAndClocks(
		ctx, check.UID, models.CheckStatusUp, 1, &realChange, models.IncidentClockUpdate{},
	))

	// Positive control: prove the fixture really does reproduce the bug, by
	// running the deleted LAG query against this very database. It must
	// report the retention horizon — if this ever stops holding, the fixture
	// has drifted and the assertions below would pass vacuously.
	r.Equal(oldestSurviving.Unix(), legacyLagStatusChange(ctx, t, dbSvc, check.UID).Unix(),
		"fixture control: the old query does return the oldest surviving raw row")

	resp, err := svc.ListChecks(ctx, org.Slug, checks.ListChecksOptions{
		IncludeLastResult:       true,
		IncludeLastStatusChange: true,
		Limit:                   10,
	})
	r.NoError(err)
	r.Len(resp.Data, 1)

	lsc := resp.Data[0].LastStatusChange
	r.NotNil(lsc, "a check with a recorded transition must carry lastStatusChange")
	r.WithinDuration(realChange, lsc.Time, time.Second,
		"must report the derived check transition, not something re-derived from raw rows")
	r.NotEqual(oldestSurviving.Unix(), lsc.Time.Unix(),
		"must NOT report the oldest surviving raw row (the retention horizon)")
	r.Equal("UP", lsc.Status)

	// The last result still comes from the raw rows, unchanged.
	r.NotNil(resp.Data[0].LastResult)
	r.WithinDuration(newest, resp.Data[0].LastResult.Timestamp, time.Second)

	// The detail path answers identically.
	detail, err := svc.GetCheck(ctx, org.Slug, check.UID, checks.GetCheckOptions{
		IncludeLastStatusChange: true,
	})
	r.NoError(err)
	r.NotNil(detail.LastStatusChange)
	r.WithinDuration(realChange, detail.LastStatusChange.Time, time.Second)
	r.Equal("UP", detail.LastStatusChange.Status)
}

// TestLastStatusChange_OmittedWhenNeverTransitioned pins the decided
// NULL-status_changed_at contract (spec 2026-08-09-07 resolved question): the
// field is omitted entirely — no fallback to created_at, no fallback to the
// raw probe history — even though raw rows exist that the old query would
// happily have mined for a "transition".
func TestLastStatusChange_OmittedWhenNeverTransitioned(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	svc, dbSvc, org := setupLastStatusChangeService(t)

	check := models.NewCheck(org.UID, "never-changed", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))
	r.Nil(check.StatusChangedAt, "fixture precondition: never transitioned")

	seedUniformRawResults(
		ctx, t, dbSvc, org.UID, check.UID, models.ResultStatusUp, 3,
		time.Now().Add(-time.Hour),
	)

	resp, err := svc.ListChecks(ctx, org.Slug, checks.ListChecksOptions{
		IncludeLastStatusChange: true,
		Limit:                   10,
	})
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Nil(resp.Data[0].LastStatusChange, "omitted when status_changed_at is NULL")

	detail, err := svc.GetCheck(ctx, org.Slug, check.UID, checks.GetCheckOptions{
		IncludeLastStatusChange: true,
	})
	r.NoError(err)
	r.Nil(detail.LastStatusChange, "omitted on the detail path too")
}

// TestLastStatusChange_NotIncludedWithoutTheFlag keeps IncludeLastStatusChange
// meaningful as a response-shaping flag now that it costs no query.
func TestLastStatusChange_NotIncludedWithoutTheFlag(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	svc, dbSvc, org := setupLastStatusChangeService(t)

	check := models.NewCheck(org.UID, "flagged", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	changedAt := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	r.NoError(dbSvc.UpdateCheckStatusAndClocks(
		ctx, check.UID, models.CheckStatusDown, 3, &changedAt, models.IncidentClockUpdate{},
	))

	resp, err := svc.ListChecks(ctx, org.Slug, checks.ListChecksOptions{Limit: 10})
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Nil(resp.Data[0].LastStatusChange, "absent unless with=last_status_change")

	withFlag, err := svc.ListChecks(ctx, org.Slug, checks.ListChecksOptions{
		IncludeLastStatusChange: true,
		Limit:                   10,
	})
	r.NoError(err)
	r.Len(withFlag.Data, 1)
	r.NotNil(withFlag.Data[0].LastStatusChange)
	r.Equal("DOWN", withFlag.Data[0].LastStatusChange.Status,
		"status mirrors the derived check status the UI shows beside it")
	r.WithinDuration(changedAt, withFlag.Data[0].LastStatusChange.Time, time.Second)
}
