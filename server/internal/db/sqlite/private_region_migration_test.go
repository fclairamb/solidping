package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// privateRegionMigrationSQL is migration 012 read straight out of the embedded
// FS, so this test can never drift from the file that actually ships. The
// migration is idempotent (every statement is guarded by `region like '@%/%'`
// or compares before writing), so re-running it after Initialize() applied it
// to an empty database is safe and is exactly how the other migration tests in
// this package work.
func privateRegionMigrationSQL(t *testing.T) string {
	t.Helper()

	body, err := migrationsFS.ReadFile("migrations/012_private_region_org_relative.up.sql")
	require.NoError(t, err)

	return string(body)
}

// TestPrivateRegionMigrationCollapsesBothSpellings is the migration half of
// spec 2026-08-13-01: every stored `@<org>/<slug>` becomes `@<slug>`, the two
// spellings of the SAME region collapse to exactly ONE array entry, and the
// rows that would collide on a unique index are de-duplicated instead of
// blowing the migration up.
func TestPrivateRegionMigrationCollapsesBothSpellings(t *testing.T) {
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

	// Two orgs, both with a private region that spells out to `@aws-paris`.
	// `renamed` was `oldname` before its rename; `other` is a bystander whose
	// rows must come out with the very same org-relative string (that is the
	// point — uniqueness now comes from organization_uid, not the string).
	exec(`insert into organizations (uid, slug, name) values ('org-renamed', 'newname', 'Renamed')`)
	exec(`insert into organizations (uid, slug, name) values ('org-other', 'other', 'Other')`)

	// A check stranded by the rename: it lists BOTH spellings of aws-paris
	// (exactly the live incident's shape) plus a cloud region.
	exec(`insert into checks (uid, organization_uid, name, type, regions)
	      values ('chk-stranded', 'org-renamed', 'stranded', 'http',
	              json_array('@oldname/aws-paris', '@newname/aws-paris', 'default'))`)
	exec(`insert into checks (uid, organization_uid, name, type, regions)
	      values ('chk-other', 'org-other', 'other', 'http',
	              json_array('@other/aws-paris'))`)
	// A cloud-only check must be left byte-identical.
	exec(`insert into checks (uid, organization_uid, name, type, regions)
	      values ('chk-cloud', 'org-renamed', 'cloud', 'http', json_array('eu-west-1', 'us-east-1'))`)

	// Two dispatch rows for the stranded check — they collapse onto one
	// (check_jobs is UNIQUE on (check_uid, region)).
	exec(`insert into check_jobs (uid, organization_uid, check_uid, region, period, scheduled_at)
	      values ('job-old', 'org-renamed', 'chk-stranded', '@oldname/aws-paris', '00:01:00', '2026-08-01T00:00:00Z')`)
	exec(`insert into check_jobs (uid, organization_uid, check_uid, region, period, scheduled_at)
	      values ('job-new', 'org-renamed', 'chk-stranded', '@newname/aws-paris', '00:01:00', '2026-08-02T00:00:00Z')`)
	exec(`insert into check_jobs (uid, organization_uid, check_uid, region, period, scheduled_at)
	      values ('job-other', 'org-other', 'chk-other', '@other/aws-paris', '00:01:00', '2026-08-01T00:00:00Z')`)

	// The agent frozen on the pre-rename spelling, plus a pending token.
	exec(`insert into agents (uid, organization_uid, region, name, ed25519_public_key, x25519_public_key, fingerprint)
	      values ('agt-1', 'org-renamed', '@oldname/aws-paris', 'a', 'ed', 'age1', 'fp')`)
	exec(`insert into agent_enrollment_tokens (uid, organization_uid, region, token_hash, expires_at)
	      values ('tok-1', 'org-renamed', '@newname/aws-paris', 'hash', '2099-01-01T00:00:00Z')`)

	// default_regions carrying both spellings must dedupe to one entry.
	exec(`insert into parameters (uid, organization_uid, key, value)
	      values ('par-1', 'org-renamed', 'default_regions',
	              json_object('value', json_array('@oldname/aws-paris', '@newname/aws-paris', 'default')))`)

	// Aggregated results on both spellings for the same bucket: they collide on
	// results_aggregated_unique_idx once collapsed, so the one with more data
	// must survive and the other must be dropped rather than failing the write.
	exec(`insert into results (uid, organization_uid, check_uid, period_type, period_start, region, total_checks)
	      values ('res-old', 'org-renamed', 'chk-stranded', 'hour', '2026-08-01T00:00:00Z', '@oldname/aws-paris', 10)`)
	exec(`insert into results (uid, organization_uid, check_uid, period_type, period_start, region, total_checks)
	      values ('res-new', 'org-renamed', 'chk-stranded', 'hour', '2026-08-01T00:00:00Z', '@newname/aws-paris', 3)`)
	// A raw row has no unique index — it is only rewritten.
	exec(`insert into results (uid, organization_uid, check_uid, period_type, period_start, region, status)
	      values ('res-raw', 'org-renamed', 'chk-stranded', 'raw', '2026-08-01T00:05:00Z', '@oldname/aws-paris', 3)`)

	exec(privateRegionMigrationSQL(t))

	scalar := func(query string, args ...any) string {
		t.Helper()

		var out string
		r.NoError(svc.DB().QueryRowContext(ctx, query, args...).Scan(&out))

		return out
	}

	count := func(query string, args ...any) int {
		t.Helper()

		var out int
		r.NoError(svc.DB().QueryRowContext(ctx, query, args...).Scan(&out))

		return out
	}

	// checks.regions: both spellings collapsed to exactly one entry, cloud
	// region preserved, order preserved.
	r.JSONEq(`["@aws-paris","default"]`, scalar(`select regions from checks where uid = 'chk-stranded'`))
	r.JSONEq(`["@aws-paris"]`, scalar(`select regions from checks where uid = 'chk-other'`))
	r.JSONEq(`["eu-west-1","us-east-1"]`, scalar(`select regions from checks where uid = 'chk-cloud'`))

	// check_jobs: one surviving row, the earliest-scheduled one.
	r.Equal(1, count(`select count(*) from check_jobs where check_uid = 'chk-stranded'`))
	r.Equal("job-old", scalar(`select uid from check_jobs where check_uid = 'chk-stranded'`))
	r.Equal("@aws-paris", scalar(`select region from check_jobs where uid = 'job-old'`))
	// The bystander org keeps its own row, under the identical string.
	r.Equal("@aws-paris", scalar(`select region from check_jobs where uid = 'job-other'`))

	// Agent and token now speak the same org-relative string as the check.
	r.Equal("@aws-paris", scalar(`select region from agents where uid = 'agt-1'`))
	r.Equal("@aws-paris", scalar(`select region from agent_enrollment_tokens where uid = 'tok-1'`))

	// default_regions deduped.
	r.JSONEq(`["@aws-paris","default"]`,
		scalar(`select json_extract(value, '$.value') from parameters where uid = 'par-1'`))

	// results: the richer aggregated row survived, the poorer one is gone, and
	// the raw row was rewritten in place.
	r.Equal(1, count(`select count(*) from results where uid in ('res-old', 'res-new')`))
	r.Equal("res-old", scalar(`select uid from results where uid in ('res-old', 'res-new')`))
	r.Equal("@aws-paris", scalar(`select region from results where uid = 'res-old'`))
	r.Equal("@aws-paris", scalar(`select region from results where uid = 'res-raw'`))

	// Re-running is a no-op: nothing left matches `@%/%`.
	exec(privateRegionMigrationSQL(t))
	r.Equal(0, count(`select count(*) from check_jobs where region like '@%/%'`))
	r.Equal(0, count(`select count(*) from agents where region like '@%/%'`))
	r.Equal(0, count(`select count(*) from results where region like '@%/%'`))
	r.Equal(1, count(`select count(*) from check_jobs where check_uid = 'chk-stranded'`))
}

// TestPrivateRegionMigrationLeavesCloudRowsAlone is the negative control for the
// migration: it must be a strict no-op on an install that has never used a
// private region, so a `LIKE '@%'` predicate can never be mistaken for "rewrite
// every region".
func TestPrivateRegionMigrationLeavesCloudRowsAlone(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	mustExec(ctx, t, svc, `insert into organizations (uid, slug, name) values ('org-c', 'cloudy', 'Cloudy')`)
	mustExec(ctx, t, svc, `insert into checks (uid, organization_uid, name, type, regions)
	      values ('chk-1', 'org-c', 'c', 'http', json_array('eu', 'eu-west-1', 'us-east-1'))`)
	mustExec(ctx, t, svc, `insert into check_jobs (uid, organization_uid, check_uid, region, period)
	      values ('job-1', 'org-c', 'chk-1', 'eu-west-1', '00:01:00')`)
	mustExec(ctx, t, svc, `insert into check_jobs (uid, organization_uid, check_uid, region, period)
	      values ('job-2', 'org-c', 'chk-1', 'us-east-1', '00:01:00')`)

	body, err := migrationsFS.ReadFile("migrations/012_private_region_org_relative.up.sql")
	r.NoError(err)

	_, err = svc.DB().ExecContext(ctx, string(body))
	r.NoError(err)

	var regionsJSON string
	r.NoError(svc.DB().QueryRowContext(ctx, `select regions from checks where uid = 'chk-1'`).Scan(&regionsJSON))
	r.JSONEq(`["eu","eu-west-1","us-east-1"]`, regionsJSON)

	var jobs int
	r.NoError(svc.DB().QueryRowContext(ctx, `select count(*) from check_jobs where check_uid = 'chk-1'`).Scan(&jobs))
	r.Equal(2, jobs, "distinct cloud regions must never be collapsed into one another")
}

// mustExec runs a statement and fails the test on error.
func mustExec(ctx context.Context, t *testing.T, svc *Service, query string) {
	t.Helper()

	_, err := svc.DB().ExecContext(ctx, query)
	require.NoError(t, err)
}
