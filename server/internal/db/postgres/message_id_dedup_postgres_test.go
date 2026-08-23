package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portMessageIDDedup is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portMessageIDDedup = 15490

// TestHasRawResultWithMessageID_Postgres is the Postgres half of the
// inbound-email dedup backstop (spec 2026-08-22-01, layer 3). The two dialects
// answer the same question through different JSON operators — `output->>'…'`
// here, `json_extract` on SQLite — and nothing but a paired test keeps them
// agreeing, since no schema constraint backs either one.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestHasRawResultWithMessageID_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     portMessageIDDedup,
		RunMode:  runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("dedup-pg-org", "Dedup PG Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "inbox-check", "email")
	other := models.NewCheck(org.UID, "other-check", "email")
	r.NoError(s.CreateCheck(ctx, check))
	r.NoError(s.CreateCheck(ctx, other))

	now := time.Now()
	window := now.Add(-7 * 24 * time.Hour)

	const known = "<known@mail.example.com>"

	seed := func(checkUID, messageID string, at time.Time, periodType string) {
		result := models.NewResult(org.UID, checkUID, models.ResultStatusUp, 0)
		result.PeriodType = periodType
		result.PeriodStart = at
		result.Output = models.JSONMap{"messageId": messageID, "message": "Email received"}
		r.NoError(s.CreateResult(ctx, result))
	}

	seed(check.UID, known, now, models.PeriodTypeRaw)

	found, err := s.HasRawResultWithMessageID(ctx, org.UID, check.UID, known, window)
	r.NoError(err)
	r.True(found, "the seeded Message-ID must be found")

	found, err = s.HasRawResultWithMessageID(ctx, org.UID, check.UID, "<never-seen@example.com>", window)
	r.NoError(err)
	r.False(found, "a different Message-ID must not match")

	found, err = s.HasRawResultWithMessageID(ctx, org.UID, other.UID, known, window)
	r.NoError(err)
	r.False(found, "the lookup must be scoped to the check")

	seed(check.UID, "<ancient@example.com>", now.Add(-30*24*time.Hour), models.PeriodTypeRaw)

	found, err = s.HasRawResultWithMessageID(ctx, org.UID, check.UID, "<ancient@example.com>", window)
	r.NoError(err)
	r.False(found, "a row older than the window must not count as a duplicate")

	seed(check.UID, "<rolled-up@example.com>", now, models.PeriodTypeHour)

	found, err = s.HasRawResultWithMessageID(ctx, org.UID, check.UID, "<rolled-up@example.com>", window)
	r.NoError(err)
	r.False(found, "only raw rows may answer the dedup question")

	found, err = s.HasRawResultWithMessageID(ctx, org.UID, check.UID, "", window)
	r.NoError(err)
	r.False(found, "an empty Message-ID must never report a duplicate")
}

// TestHasRawResultWithMessageIDUsesTheRawIndex_Postgres is the Postgres twin
// of the SQLite plan test: the organization_uid clause the query does not
// logically need is what lets the planner seek results_raw_idx. Without it PG
// can only walk the whole partial index (or lean on version-dependent
// skip-scan) on every single inbound email.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestHasRawResultWithMessageIDUsesTheRawIndex_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     portMessageIDDedup + 1,
		RunMode:  runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("dedup-plan-pg-org", "Dedup Plan PG Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "plan-check", "email")
	r.NoError(s.CreateCheck(ctx, check))

	now := time.Now()

	for i := range 200 {
		result := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0)
		result.PeriodStart = now.Add(-time.Duration(i) * time.Minute)
		result.Output = models.JSONMap{"messageId": "<bulk@example.com>"}
		r.NoError(s.CreateResult(ctx, result))
	}

	_, err = s.DB().ExecContext(ctx, "ANALYZE results")
	r.NoError(err)

	// Explain the EXACT SQL the production path emits, args inlined, so this
	// cannot drift from HasRawResultWithMessageID by transcription.
	query := s.DB().NewSelect().
		Model((*models.Result)(nil)).
		Where("organization_uid = ?", org.UID).
		Where("check_uid = ?", check.UID).
		Where("period_type = ?", models.PeriodTypeRaw).
		Where("period_start >= ?", now.Add(-time.Hour)).
		Where("output->>'messageId' = ?", "<bulk@example.com>")

	sqlBytes, err := query.AppendQuery(s.DB().QueryGen(), nil)
	r.NoError(err)

	// An embedded test database is far too small for the planner to prefer any
	// index, so the question asked here is the one that survives production
	// data volumes: CAN this predicate be answered by seeking results_raw_idx,
	// and does the seek use the leading columns rather than filtering after
	// the fact? enable_seqscan=off forces the planner to show its best index
	// plan; the Index Cond is where the answer actually is.
	conn, err := s.DB().Conn(ctx)
	r.NoError(err)

	defer func() { _ = conn.Close() }()

	_, err = conn.ExecContext(ctx, "SET enable_seqscan = off")
	r.NoError(err)

	rows, err := conn.QueryContext(ctx, "EXPLAIN "+string(sqlBytes))
	r.NoError(err)

	defer func() { _ = rows.Close() }()

	var lines []string

	for rows.Next() {
		var line string
		r.NoError(rows.Scan(&line))

		lines = append(lines, line)
	}

	r.NoError(rows.Err())

	plan := strings.Join(lines, "\n")

	r.Contains(plan, "results_raw_idx", "the dedup lookup must be answerable by the raw index:\n"+plan)

	indexCond := ""

	for _, line := range lines {
		if strings.Contains(line, "Index Cond:") {
			indexCond = line
		}
	}

	r.NotEmpty(indexCond, "the plan must carry an Index Cond, not filter everything after the scan:\n"+plan)
	r.Contains(indexCond, "organization_uid",
		"organization_uid must be part of the index seek, not a post-scan filter:\n"+plan)
	r.Contains(indexCond, "check_uid", "check_uid must be part of the index seek:\n"+plan)
	r.Contains(indexCond, "period_start", "the window must be part of the index seek:\n"+plan)
}
