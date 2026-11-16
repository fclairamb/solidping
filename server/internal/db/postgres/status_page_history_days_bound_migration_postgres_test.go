package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// TestStatusPageHistoryDaysBoundMigration_Postgres is the Postgres twin of the
// SQLite test: spec 2026-11-16-01's data repair rewrites a history_days outside
// 1..90 with the day count of the row's history_period, and leaves an in-range
// row alone.
func TestStatusPageHistoryDaysBoundMigration_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: testsupport.FreePort(t), RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	migration := migrationSection(t, "status-page-history-days-bound")

	exec := func(query string, args ...any) {
		t.Helper()

		_, execErr := svc.DB().ExecContext(ctx, query, args...)
		r.NoError(execErr)
	}

	days := func(uid string) int {
		t.Helper()

		var out int
		r.NoError(svc.DB().QueryRowContext(ctx,
			`select history_days from status_pages where uid = ?`, uid).Scan(&out))

		return out
	}

	org := "11111111-1111-1111-1111-111111111111"
	exec(`insert into organizations (uid, slug, name) values (?, 'acme', 'Acme')`, org)

	rows := []struct {
		uid, slug, period string
		days              int
	}{
		{"22222222-2222-2222-2222-222222222222", "huge", "90d", 2000000000},
		{"33333333-3333-3333-3333-333333333333", "negative", "7d", -1},
		{"44444444-4444-4444-4444-444444444444", "zero", "24h", 0},
		{"55555555-5555-5555-5555-555555555555", "over", "30d", 91},
		{"66666666-6666-6666-6666-666666666666", "fine", "90d", 60},
	}
	for _, row := range rows {
		exec(`insert into status_pages (uid, organization_uid, name, slug, history_period, history_days)
		      values (?, ?, 'Page', ?, ?, ?)`, row.uid, org, row.slug, row.period, row.days)
	}

	_, err = svc.DB().ExecContext(ctx, migration)
	r.NoError(err)

	r.Equal(90, days(rows[0].uid))
	r.Equal(7, days(rows[1].uid))
	r.Equal(1, days(rows[2].uid))
	r.Equal(30, days(rows[3].uid))
	r.Equal(60, days(rows[4].uid), "an in-range row must be left alone")

	_, err = svc.DB().ExecContext(ctx, migration)
	r.NoError(err)
	r.Equal(90, days(rows[0].uid))
	r.Equal(60, days(rows[4].uid))
}
