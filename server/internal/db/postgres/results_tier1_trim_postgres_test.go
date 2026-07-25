package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Ports below are distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const (
	portResultsProjection = 15462
	portResultsMigration  = 15463
	portResultsSavePath   = 15464
)

// newTier1ServicePG starts embedded Postgres and returns a ready Service,
// self-skipping (like every embedded-postgres test in this package) under
// -short or when embedded Postgres is unavailable.
func newTier1ServicePG(t *testing.T, port uint32) *Service {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	s, err := New(ctx, &Config{Embedded: true, Port: port, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	return s
}

// TestListResults_SkipBlobsProjection_Postgres is the Postgres counterpart to
// the SQLite projection test: SkipBlobs must drop metrics/output from the
// SELECT while every other column survives (spec 2026-07-24-02 §5). The
// default-projection run is the positive control.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestListResults_SkipBlobsProjection_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portResultsProjection)

	org := models.NewOrganization("projection-org-pg", "Projection Org PG")
	r.NoError(s.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "projection-check-pg", "http")
	r.NoError(s.CreateCheck(ctx, check))

	seeded := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 123)
	seeded.PeriodStart = time.Now().Add(time.Hour)
	seeded.Metrics = models.JSONMap{"handshake_ms": 12.5}
	seeded.Output = models.JSONMap{"body": "hello", "headers": "a-fairly-large-blob"}
	r.NoError(s.CreateResult(ctx, seeded))

	list := func(skipBlobs bool) *models.Result {
		resp, listErr := s.ListResults(ctx, &models.ListResultsFilter{
			OrganizationUID: org.UID,
			CheckUIDs:       []string{check.UID},
			PeriodTypes:     []string{models.PeriodTypeRaw},
			Limit:           10,
			SkipBlobs:       skipBlobs,
		})
		r.NoError(listErr)

		for _, row := range resp.Results {
			if row.UID == seeded.UID {
				return row
			}
		}

		r.FailNow("seeded result not returned by ListResults")

		return nil
	}

	full := list(false)
	r.InDelta(12.5, full.Metrics["handshake_ms"], 0.001, "default projection must return metrics")
	r.Equal("hello", full.Output["body"], "default projection must return output")

	trimmed := list(true)
	r.Empty(trimmed.Metrics, "SkipBlobs must leave metrics empty")
	r.Empty(trimmed.Output, "SkipBlobs must leave output empty")

	r.Equal(seeded.UID, trimmed.UID)
	r.Equal(check.UID, trimmed.CheckUID)
	r.Equal(models.PeriodTypeRaw, trimmed.PeriodType)
	r.NotNil(trimmed.Status)
	r.Equal(int(models.ResultStatusUp), *trimmed.Status)
	r.NotNil(trimmed.Duration)
	r.InDelta(123, *trimmed.Duration, 0.001)
}

// TestMigration009ResultsTrimUpDown_Postgres proves migration 009_v0_8_0 drops
// results.last_for_status (+ its partial index) and results.availability_pct on
// Postgres, that its down migration restores all three, and that re-applying up
// drops them again (spec 2026-07-24-02).
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestMigration009ResultsTrimUpDown_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	// Initialize (inside the helper) runs every embedded migration, 009 included.
	s := newTier1ServicePG(t, portResultsMigration)

	resultsColumns := func() []string {
		var names []string
		r.NoError(s.db.NewRaw(
			"SELECT column_name FROM information_schema.columns "+
				"WHERE table_schema = 'public' AND table_name = 'results'",
		).Scan(ctx, &names))

		return names
	}

	indexExists := func(name string) bool {
		var count int
		r.NoError(s.db.NewRaw(
			"SELECT count(*) FROM pg_indexes WHERE schemaname = 'public' AND indexname = ?", name,
		).Scan(ctx, &count))

		return count == 1
	}

	execMigration := func(file string) {
		content, readErr := migrationsFS.ReadFile(file)
		r.NoError(readErr)
		_, execErr := s.db.ExecContext(ctx, string(content))
		r.NoError(execErr, "%s must apply cleanly", file)
	}

	cols := resultsColumns()
	r.NotContains(cols, "last_for_status", "009 up must drop results.last_for_status")
	r.NotContains(cols, "availability_pct", "009 up must drop results.availability_pct")
	r.False(indexExists("idx_results_last_for_status"), "009 up must drop the partial index")
	r.Contains(cols, "total_checks", "009 must not touch the other aggregate columns")
	r.Contains(cols, "successful_checks", "009 must not touch the other aggregate columns")
	r.Contains(cols, "metrics", "009 must not touch the blob columns")
	r.Contains(cols, "output", "009 must not touch the blob columns")

	execMigration("migrations/009_v0_8_0.down.sql")

	cols = resultsColumns()
	r.Contains(cols, "last_for_status", "009 down must restore results.last_for_status")
	r.Contains(cols, "availability_pct", "009 down must restore results.availability_pct")
	r.True(indexExists("idx_results_last_for_status"), "009 down must restore the partial index")

	execMigration("migrations/009_v0_8_0.up.sql")

	cols = resultsColumns()
	r.NotContains(cols, "last_for_status")
	r.NotContains(cols, "availability_pct")
	r.False(indexExists("idx_results_last_for_status"))
}

// TestSaveResultWithStatusTracking_SingleInsert_Postgres proves the result save
// path is exactly one INSERT with no companion UPDATE (spec 2026-07-24-02 §1),
// which is where half the write-side bloat of the raw tier came from.
//
// The proof is a statement-level BEFORE UPDATE trigger on `results` that raises:
// it fires on any UPDATE statement against the table, even one matching zero
// rows, so re-introducing the old flag-clearing UPDATE turns the save into an
// error instead of a silently-passing test.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestSaveResultWithStatusTracking_SingleInsert_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portResultsSavePath)

	org := models.NewOrganization("save-path-org-pg", "Save Path Org PG")
	r.NoError(s.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "save-path-check-pg", "http")
	r.NoError(s.CreateCheck(ctx, check))

	_, err := s.db.ExecContext(ctx, `
		create function results_forbid_update() returns trigger as $$
		begin
			raise exception 'unexpected UPDATE on results';
		end;
		$$ language plpgsql;

		create trigger results_no_update before update on results
			for each statement execute function results_forbid_update();
	`)
	r.NoError(err)

	base := time.Now().Add(time.Hour)

	first := models.NewResult(org.UID, check.UID, models.ResultStatusDown, 10)
	first.PeriodStart = base
	r.NoError(s.SaveResultWithStatusTracking(ctx, first),
		"the save path must be a plain INSERT (no UPDATE on results)")

	// A second save with the *same* status is the case the old flag-clearing
	// UPDATE targeted.
	second := models.NewResult(org.UID, check.UID, models.ResultStatusDown, 20)
	second.PeriodStart = base.Add(time.Minute)
	r.NoError(s.SaveResultWithStatusTracking(ctx, second),
		"a second same-status save must not rewrite its predecessor")

	resp, err := s.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		PeriodTypes:     []string{models.PeriodTypeRaw},
		Statuses:        []int{int(models.ResultStatusDown)},
		Limit:           10,
	})
	r.NoError(err)
	r.Len(resp.Results, 2, "both saved results must persist")

	byUID := make(map[string]*models.Result, len(resp.Results))
	for _, row := range resp.Results {
		byUID[row.UID] = row
	}

	stored := byUID[first.UID]
	r.NotNil(stored, "the first result must still be there")
	r.NotNil(stored.Status)
	r.Equal(int(models.ResultStatusDown), *stored.Status)
	r.NotNil(stored.Duration)
	r.InDelta(10, *stored.Duration, 0.001)
	r.NotNil(byUID[second.UID], "the second result must be there too")

	// Positive control: the guard really does fire on an UPDATE against results,
	// so the two clean saves above are evidence and not a no-op.
	_, err = s.db.ExecContext(ctx, "update results set status = status where uid = ?", first.UID)
	r.Error(err, "the UPDATE guard must fire — otherwise this test proves nothing")

	_, err = s.db.ExecContext(ctx, "drop trigger results_no_update on results")
	r.NoError(err)
}
