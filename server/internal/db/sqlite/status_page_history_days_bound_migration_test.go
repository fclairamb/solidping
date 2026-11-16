package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStatusPageHistoryDaysBoundMigration pins spec 2026-11-16-01's data
// repair: a history_days outside 1..90 is rewritten with the day count of the
// row's history_period, and an in-range row is untouched.
func TestStatusPageHistoryDaysBoundMigration(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

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

	exec(`insert into organizations (uid, slug, name) values ('org-1', 'acme', 'Acme')`)

	rows := []struct {
		uid, slug, period string
		days              int
	}{
		{"page-huge", "huge", "90d", 2000000000},
		{"page-negative", "negative", "7d", -1},
		{"page-zero", "zero", "24h", 0},
		{"page-over", "over", "30d", 91},
		{"page-ok", "fine", "90d", 60},
	}
	for _, row := range rows {
		exec(`insert into status_pages (uid, organization_uid, name, slug, history_period, history_days)
		      values (?, 'org-1', 'Page', ?, ?, ?)`, row.uid, row.slug, row.period, row.days)
	}

	migration := migrationSection(t, "status-page-history-days-bound")

	_, err = svc.DB().ExecContext(ctx, migration)
	r.NoError(err)

	r.Equal(90, days("page-huge"))
	r.Equal(7, days("page-negative"))
	r.Equal(1, days("page-zero"))
	r.Equal(30, days("page-over"))
	r.Equal(60, days("page-ok"), "an in-range row must be left alone")

	// Idempotent.
	_, err = svc.DB().ExecContext(ctx, migration)
	r.NoError(err)
	r.Equal(90, days("page-huge"))
	r.Equal(60, days("page-ok"))
}
