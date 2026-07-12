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
