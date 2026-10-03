package sqlite

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// multistepSection returns the statements of the multistep-bulk-checks
// SECTION of a 026 migration file, split on --bun:split.
func multistepSection(t *testing.T, path string) []string {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	text := string(raw)
	start := strings.Index(text, "SECTION: multistep-bulk-checks")
	require.GreaterOrEqual(t, start, 0, "%s must carry the multistep-bulk-checks section", path)

	section := text[start:]
	section = section[strings.Index(section, "\n")+1:] // drop the marker's own comment line

	if end := strings.Index(section, "SECTION:"); end >= 0 {
		section = section[:end]
	}

	var statements []string

	for _, chunk := range strings.Split(section, "--bun:split") {
		var lines []string

		for _, line := range strings.Split(chunk, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}

			lines = append(lines, line)
		}

		if len(lines) > 0 {
			statements = append(statements, strings.Join(lines, "\n"))
		}
	}

	return statements
}

// The multistep-bulk-checks section of 026 (spec 2026-10-03-03) adds the five
// check_jobs.step_* columns and the bulk claim index; its down half removes
// them, and up applies again cleanly.
func TestMigration026MultistepUpDown(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	columns := func() []string {
		var names []string
		r.NoError(svc.db.NewRaw("SELECT name FROM pragma_table_info('check_jobs')").Scan(ctx, &names))

		return names
	}

	indexExists := func() bool {
		var count int
		r.NoError(svc.db.NewRaw(
			"SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_check_jobs_claim_bulk'",
		).Scan(ctx, &count))

		return count == 1
	}

	stepColumns := []string{"step_run_uid", "step_run_started_at", "step_count", "step_state_file_uid", "step_failures"}

	apply := func(statements []string) {
		for _, statement := range statements {
			_, execErr := svc.db.ExecContext(ctx, statement)
			r.NoError(execErr, statement)
		}
	}

	for _, column := range stepColumns {
		r.Contains(columns(), column)
	}

	r.True(indexExists())

	apply(multistepSection(t, "migrations/026_v0_38_0.down.sql"))

	for _, column := range stepColumns {
		r.NotContains(columns(), column)
	}

	r.False(indexExists())
	r.Contains(columns(), "lane", "down must not touch the rest of check_jobs")

	apply(multistepSection(t, "migrations/026_v0_38_0.up.sql"))

	for _, column := range stepColumns {
		r.Contains(columns(), column)
	}

	r.True(indexExists())
}
