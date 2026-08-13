package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// portPrivateRegionMigration is distinct from every other _postgres_test.go
// embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portPrivateRegionMigration = 15469

// TestPrivateRegionMigrationCollapsesBothSpellings_Postgres is the Postgres twin
// of the SQLite migration test. The two dialects express the rewrite completely
// differently (split_part + unnest WITH ORDINALITY + DELETE…USING here,
// substr/instr + json_each + DELETE…WHERE uid IN there), so each needs its own
// proof — and Postgres additionally enforces the unique indexes the
// de-duplication exists to protect, so a missing dedupe fails LOUDLY here.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestPrivateRegionMigrationCollapsesBothSpellings_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portPrivateRegionMigration, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	migration, err := migrationsFS.ReadFile("migrations/012_private_region_org_relative.up.sql")
	r.NoError(err)

	exec := func(query string, args ...any) {
		t.Helper()

		_, execErr := svc.DB().ExecContext(ctx, query, args...)
		r.NoError(execErr)
	}

	orgRenamed := "11111111-1111-1111-1111-111111111111"
	orgOther := "22222222-2222-2222-2222-222222222222"
	chkStranded := "33333333-3333-3333-3333-333333333333"
	chkOther := "44444444-4444-4444-4444-444444444444"

	exec(`insert into organizations (uid, slug, name) values (?, 'newname', 'Renamed')`, orgRenamed)
	exec(`insert into organizations (uid, slug, name) values (?, 'other', 'Other')`, orgOther)

	exec(`insert into checks (uid, organization_uid, name, type, regions)
	      values (?, ?, 'stranded', 'http', ?::text[])`,
		chkStranded, orgRenamed, `{"@oldname/aws-paris","@newname/aws-paris","default"}`)
	exec(`insert into checks (uid, organization_uid, name, type, regions)
	      values (?, ?, 'other', 'http', ?::text[])`,
		chkOther, orgOther, `{"@other/aws-paris"}`)

	exec(`insert into check_jobs (uid, organization_uid, check_uid, region, period, scheduled_at)
	      values (gen_random_uuid(), ?, ?, '@oldname/aws-paris', '1 minute', '2026-08-01T00:00:00Z')`,
		orgRenamed, chkStranded)
	exec(`insert into check_jobs (uid, organization_uid, check_uid, region, period, scheduled_at)
	      values (gen_random_uuid(), ?, ?, '@newname/aws-paris', '1 minute', '2026-08-02T00:00:00Z')`,
		orgRenamed, chkStranded)
	exec(`insert into check_jobs (uid, organization_uid, check_uid, region, period, scheduled_at)
	      values (gen_random_uuid(), ?, ?, '@other/aws-paris', '1 minute', '2026-08-01T00:00:00Z')`,
		orgOther, chkOther)

	exec(`insert into agents (uid, organization_uid, region, name, ed25519_public_key, x25519_public_key, fingerprint)
	      values (gen_random_uuid(), ?, '@oldname/aws-paris', 'a', 'ed', 'age1', 'fp')`, orgRenamed)
	exec(`insert into agent_enrollment_tokens (uid, organization_uid, region, token_hash, expires_at)
	      values (gen_random_uuid(), ?, '@newname/aws-paris', 'hash', now() + interval '1 year')`, orgRenamed)

	exec(`insert into parameters (uid, organization_uid, key, value)
	      values (gen_random_uuid(), ?, 'default_regions', ?::jsonb)`,
		orgRenamed, `{"value":["@oldname/aws-paris","@newname/aws-paris","default"]}`)

	exec(`insert into results (uid, organization_uid, check_uid, period_type, period_start, region, total_checks)
	      values (gen_random_uuid(), ?, ?, 'hour', '2026-08-01T00:00:00Z', '@oldname/aws-paris', 10)`,
		orgRenamed, chkStranded)
	exec(`insert into results (uid, organization_uid, check_uid, period_type, period_start, region, total_checks)
	      values (gen_random_uuid(), ?, ?, 'hour', '2026-08-01T00:00:00Z', '@newname/aws-paris', 3)`,
		orgRenamed, chkStranded)

	// The migration itself. A missing de-duplication would raise a unique
	// violation right here rather than producing a wrong-but-quiet result.
	_, err = svc.DB().ExecContext(ctx, string(migration))
	r.NoError(err)

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

	r.Equal("{@aws-paris,default}", scalar(`select regions::text from checks where uid = ?`, chkStranded))
	r.Equal("{@aws-paris}", scalar(`select regions::text from checks where uid = ?`, chkOther))

	r.Equal(1, count(`select count(*) from check_jobs where check_uid = ?`, chkStranded))
	r.Equal("@aws-paris", scalar(`select region from check_jobs where check_uid = ?`, chkStranded))
	// The bystander org keeps its own row under the byte-identical string:
	// isolation now comes from organization_uid, not from the string.
	r.Equal("@aws-paris", scalar(`select region from check_jobs where check_uid = ?`, chkOther))

	r.Equal("@aws-paris", scalar(`select region from agents where organization_uid = ?`, orgRenamed))
	r.Equal("@aws-paris",
		scalar(`select region from agent_enrollment_tokens where organization_uid = ?`, orgRenamed))

	r.JSONEq(`["@aws-paris","default"]`, scalar(
		`select (value -> 'value')::text from parameters where organization_uid = ? and key = 'default_regions'`,
		orgRenamed))

	// One aggregated row survives the collapse — the one with the most data.
	r.Equal(1, count(`select count(*) from results where check_uid = ? and period_type = 'hour'`, chkStranded))
	r.Equal(10, count(
		`select total_checks from results where check_uid = ? and period_type = 'hour'`, chkStranded))
	r.Equal("@aws-paris", scalar(
		`select region from results where check_uid = ? and period_type = 'hour'`, chkStranded))

	// Idempotent.
	_, err = svc.DB().ExecContext(ctx, string(migration))
	r.NoError(err)
	r.Equal(0, count(`select count(*) from check_jobs where region like '@%/%'`))
	r.Equal(0, count(`select count(*) from agents where region like '@%/%'`))
	r.Equal(1, count(`select count(*) from check_jobs where check_uid = ?`, chkStranded))
}
