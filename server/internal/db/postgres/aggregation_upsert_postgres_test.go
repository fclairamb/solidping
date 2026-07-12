package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portAggUpsert is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portAggUpsert = 15451

// TestUpsertAggregatedResult_Idempotent_Postgres is the Postgres counterpart to
// the SQLite idempotency test for spec 2026-07-11-16 §3: two writes of the same
// aggregated bucket yield one row, for NULL region (the former unique-index
// hole) and for a set region alike. Self-skips under -short / on
// embedded-startup error, like every embedded-postgres test in this package.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction)
func TestUpsertAggregatedResult_Idempotent_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{Embedded: true, Port: portAggUpsert, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("agg-upsert-pg-org", "Agg Upsert PG Org")
	r.NoError(s.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "agg-upsert-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	periodStart := time.Now().UTC().Add(-30 * time.Hour).Truncate(time.Hour)
	periodEnd := periodStart.Add(time.Hour)

	build := func(region *string, totalChecks int) *models.Result {
		status := int(models.ResultStatusUp)
		count := totalChecks
		end := periodEnd

		return &models.Result{
			UID:             uuid.Must(uuid.NewV7()).String(),
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			PeriodType:      models.PeriodTypeHour,
			PeriodStart:     periodStart,
			PeriodEnd:       &end,
			Region:          region,
			Status:          &status,
			TotalChecks:     &count,
			CreatedAt:       time.Now(),
		}
	}

	r.NoError(s.UpsertAggregatedResult(ctx, build(nil, 5)))
	r.NoError(s.UpsertAggregatedResult(ctx, build(nil, 9)))

	region := "eu"
	r.NoError(s.UpsertAggregatedResult(ctx, build(&region, 3)))
	r.NoError(s.UpsertAggregatedResult(ctx, build(&region, 7)))

	resp, err := s.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		PeriodTypes:     []string{models.PeriodTypeHour},
		Limit:           100,
	})
	r.NoError(err)
	r.Len(resp.Results, 2, "one row per bucket key (NULL region + eu); NULLs must not duplicate")

	for _, row := range resp.Results {
		r.NotNil(row.TotalChecks)
		if row.Region == nil {
			r.Equal(9, *row.TotalChecks, "NULL-region row reflects the last upsert")
		} else {
			r.Equal(7, *row.TotalChecks, "eu row reflects the last upsert")
		}
	}
}

// TestListResults_ExcludeStatuses_Postgres pins the Postgres side of spec
// 2026-07-11-16 §1: ExcludeStatuses drops the listed statuses (lifecycle
// markers) while keeping everything else.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction)
func TestListResults_ExcludeStatuses_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{Embedded: true, Port: portAggUpsert, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("agg-exclude-pg-org", "Agg Exclude PG Org")
	r.NoError(s.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "agg-exclude-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	base := time.Now().UTC().Add(-30 * time.Hour).Truncate(time.Hour)
	seed := func(status models.ResultStatus, offset time.Duration) {
		res := models.NewResult(org.UID, check.UID, status, 1.0)
		res.PeriodStart = base.Add(offset)
		r.NoError(s.CreateResult(ctx, res))
	}
	seed(models.ResultStatusCreated, 1*time.Minute)
	seed(models.ResultStatusRunning, 2*time.Minute)
	seed(models.ResultStatusUp, 3*time.Minute)

	resp, err := s.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		PeriodTypes:     []string{models.PeriodTypeRaw},
		ExcludeStatuses: []int{int(models.ResultStatusCreated), int(models.ResultStatusRunning)},
		Limit:           100,
	})
	r.NoError(err)
	r.Len(resp.Results, 1, "only the non-marker row must survive the exclusion")
	r.Equal(int(models.ResultStatusUp), *resp.Results[0].Status)
}

// TestListResults_RequireCheckExists_Postgres is the Postgres counterpart to the
// SQLite RequireCheckExists filter test (spec 2026-07-12-01 §5). Postgres enforces
// results.check_uid → checks.uid at the schema level, so it cannot normally hold
// FK-orphans; to prove the clause bites we seed one the way the production
// orphans arose — an insert on a connection with FK triggers disabled
// (session_replication_role = replica, the Postgres analog of the SQLite
// PRAGMA foreign_keys = OFF used by the migration-007 orphan seed). With
// RequireCheckExists:false the orphan is visible; with true it is excluded while a
// live check's row survives — defense in depth, kept in lockstep with SQLite.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction)
func TestListResults_RequireCheckExists_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{Embedded: true, Port: portAggUpsert, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("agg-orphan-pg-org", "Agg Orphan PG Org")
	r.NoError(s.CreateOrganization(ctx, org))

	liveCheck := models.NewCheck(org.UID, "agg-orphan-live-check", "http")
	r.NoError(s.CreateCheck(ctx, liveCheck))

	base := time.Now().UTC().Add(-30 * time.Hour).Truncate(time.Hour)

	live := models.NewResult(org.UID, liveCheck.UID, models.ResultStatusUp, 1.0)
	live.PeriodStart = base.Add(2 * time.Minute)
	r.NoError(s.CreateResult(ctx, live))

	// Seed an FK-orphan raw result whose check_uid has no checks row.
	orphanCheckUID := uuid.Must(uuid.NewV7()).String()
	orphanUID := uuid.Must(uuid.NewV7()).String()
	seedOrphanResultPG(t, s, org.UID, orphanCheckUID, orphanUID, base.Add(5*time.Minute))

	listUIDs := func(requireCheckExists bool) map[string]bool {
		resp, listErr := s.ListResults(ctx, &models.ListResultsFilter{
			OrganizationUID:    org.UID,
			PeriodTypes:        []string{models.PeriodTypeRaw},
			RequireCheckExists: requireCheckExists,
			Limit:              100,
		})
		r.NoError(listErr)

		set := make(map[string]bool, len(resp.Results))
		for _, row := range resp.Results {
			set[row.UID] = true
		}

		return set
	}

	// Without the guard, the orphan is visible alongside the live row.
	unguarded := listUIDs(false)
	r.Contains(unguarded, live.UID, "control: the live row is visible")
	r.Contains(unguarded, orphanUID, "control: the orphan is visible without the guard")

	// With the guard, only the live check's row survives.
	guarded := listUIDs(true)
	r.Contains(guarded, live.UID, "a live check's rows must stay visible")
	r.NotContains(guarded, orphanUID, "an orphaned row must be excluded")
}

// seedOrphanResultPG inserts one raw result referencing a non-existent check on a
// single pinned connection with FK triggers disabled
// (session_replication_role = replica), so the results.check_uid → checks.uid
// foreign key does not reject it. The role is restored and the connection
// released before returning, so no pooled connection keeps the relaxed setting.
func seedOrphanResultPG(
	t *testing.T, s *Service, orgUID, checkUID, resultUID string, periodStart time.Time,
) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	conn, err := s.db.Conn(ctx)
	r.NoError(err)
	defer func() { _ = conn.Close() }()

	_, err = conn.NewRaw("SET session_replication_role = replica").Exec(ctx)
	r.NoError(err)

	_, err = conn.NewRaw(
		"INSERT INTO results (uid, organization_uid, check_uid, period_type, period_start, status) "+
			"VALUES (?, ?, ?, 'raw', ?, 3)",
		resultUID, orgUID, checkUID, periodStart,
	).Exec(ctx)
	r.NoError(err)

	_, err = conn.NewRaw("SET session_replication_role = DEFAULT").Exec(ctx)
	r.NoError(err)
}
