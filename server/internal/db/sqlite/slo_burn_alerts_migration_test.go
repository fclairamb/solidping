package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// sectionSLOBurn is the section spec 2026-08-21-08 contributes to 015_v0_18_0.
const sectionSLOBurn = "slo-burn-alerts"

// TestSLOBurnAlertsSectionShipsBothDialectHalves is the cheap guard against the
// single most common migration mistake in this repo: adding a section to
// Postgres and forgetting SQLite. It also pins the section name, which the rest
// of these tests slice on.
func TestSLOBurnAlertsSectionShipsBothDialectHalves(t *testing.T) {
	t.Parallel()

	up := migrationSection(t, sectionSLOBurn)
	require.Contains(t, up, "slo_alert_policies")
	require.Contains(t, up, "uq_active_slo_burn_incident")

	down := findMigrationSection(t, "down", sectionSLOBurn)
	require.Contains(t, down, "drop table if exists slo_alert_policies")
}

// TestSLOBurnAlertsMigrationShapesTheTables verifies the columns the evaluator
// and the alerting API actually read, rather than merely that the migration
// ran.
func TestSLOBurnAlertsMigrationShapesTheTables(t *testing.T) {
	t.Parallel()

	ctx, svc, _ := migratedSQLite(t)

	policies := tableColumns(ctx, t, svc, "slo_alert_policies")
	for _, column := range []string{
		"uid", "organization_uid", "slo_uid", "kind", "enabled",
		"long_window_seconds", "short_window_seconds", "threshold", "severity",
		"min_samples", "last_evaluated_at", "last_long_burn_rate",
		"last_short_burn_rate", "below_threshold_since",
	} {
		require.True(t, policies[column], "slo_alert_policies must carry %s", column)
	}

	incidents := tableColumns(ctx, t, svc, "incidents")
	for _, column := range []string{"kind", "slo_uid", "slo_alert_policy_uid"} {
		require.True(t, incidents[column], "incidents must carry %s", column)
	}
}

// TestExistingIncidentsDefaultToTheCheckKind: `kind` has to be backfill-free.
// A row inserted the way every pre-existing incident was must keep meaning
// "a failing check", or the check state machine's kind-filtered lookups would
// stop seeing history.
func TestExistingIncidentsDefaultToTheCheckKind(t *testing.T) {
	t.Parallel()

	ctx, svc, org := migratedSQLite(t)
	r := require.New(t)

	_, err := svc.DB().ExecContext(ctx,
		`insert into checks (uid, organization_uid, slug, type) values ('c1', ?, 'api', 'http')`, org)
	r.NoError(err)

	_, err = svc.DB().ExecContext(ctx,
		`insert into incidents (uid, organization_uid, check_uid, state, started_at, number)
		 values ('i1', ?, 'c1', 1, datetime('now'), 1)`, org)
	r.NoError(err)

	var kind string

	r.NoError(svc.DB().QueryRowContext(ctx, `select kind from incidents where uid = 'i1'`).Scan(&kind))
	r.Equal("check", kind)
}

// TestOnlyOneOpenBurnIncidentPerSLOAndPolicy is the dedup guarantee, enforced
// by the database rather than only by the evaluator: two replicas racing on the
// same minute both read "nothing open" and both insert, and exactly one must
// win.
func TestOnlyOneOpenBurnIncidentPerSLOAndPolicy(t *testing.T) {
	t.Parallel()

	ctx, svc, org := migratedSQLite(t)
	r := require.New(t)

	_, err := svc.DB().ExecContext(ctx,
		`insert into checks (uid, organization_uid, slug, type) values ('c1', ?, 'api', 'http')`, org)
	r.NoError(err)

	_, err = svc.DB().ExecContext(ctx,
		`insert into slos (uid, organization_uid, name, slug, check_uid, target_pct)
		 values ('s1', ?, 'api', 'api', 'c1', 99.9)`, org)
	r.NoError(err)

	_, err = svc.DB().ExecContext(ctx,
		`insert into slo_alert_policies
		   (uid, organization_uid, slo_uid, kind, long_window_seconds, short_window_seconds, threshold, severity)
		 values ('p1', ?, 's1', 'fast', 3600, 300, 14.4, 'critical')`, org)
	r.NoError(err)

	insert := func(uid string, number int, state int) error {
		_, execErr := svc.DB().ExecContext(ctx,
			`insert into incidents
			   (uid, organization_uid, check_uid, state, started_at, number, kind, slo_uid, slo_alert_policy_uid)
			 values (?, ?, 'c1', ?, datetime('now'), ?, 'slo_burn', 's1', 'p1')`,
			uid, org, state, number)

		return execErr
	}

	r.NoError(insert("i1", 1, 1))
	r.Error(insert("i2", 2, 1), "a second OPEN burn incident for the same slo+policy must be refused")

	// A resolved one is fine: the index only covers state = 1, so history
	// accumulates normally.
	r.NoError(insert("i3", 3, 2))
}

// TestPolicyKindIsUniquePerSLO: the two built-ins are named identities, not a
// free-form list, so a duplicate "fast" must be impossible.
func TestPolicyKindIsUniquePerSLO(t *testing.T) {
	t.Parallel()

	ctx, svc, org := migratedSQLite(t)
	r := require.New(t)

	_, err := svc.DB().ExecContext(ctx,
		`insert into checks (uid, organization_uid, slug, type) values ('c1', ?, 'api', 'http')`, org)
	r.NoError(err)

	_, err = svc.DB().ExecContext(ctx,
		`insert into slos (uid, organization_uid, name, slug, check_uid, target_pct)
		 values ('s1', ?, 'api', 'api', 'c1', 99.9)`, org)
	r.NoError(err)

	insert := func(uid, kind string) error {
		_, execErr := svc.DB().ExecContext(ctx,
			`insert into slo_alert_policies
			   (uid, organization_uid, slo_uid, kind, long_window_seconds, short_window_seconds, threshold, severity)
			 values (?, ?, 's1', ?, 3600, 300, 14.4, 'critical')`, uid, org, kind)

		return execErr
	}

	r.NoError(insert("p1", "fast"))
	r.NoError(insert("p2", "slow"))
	r.Error(insert("p3", "fast"))
}
