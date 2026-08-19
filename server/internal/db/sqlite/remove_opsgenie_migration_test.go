package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// removeOpsgenieMigrationSQL is migration 015 read straight out of the
// embedded FS, so this test can never drift from the file that actually
// ships. The migration is idempotent (a DELETE ... WHERE type = 'opsgenie'
// against rows that no longer exist is a no-op), so re-running it after
// Initialize() already applied it to an empty database is safe — same
// pattern as the other migration tests in this package (e.g.
// private_region_migration_test.go).
func removeOpsgenieMigrationSQL(t *testing.T) string {
	t.Helper()

	body, err := migrationsFS.ReadFile("migrations/015_remove_opsgenie_integrations.up.sql")
	require.NoError(t, err)

	return string(body)
}

// TestRemoveOpsgenieMigrationDeletesIntegrationAndDependents pins spec
// 2026-08-19-02's data migration: an org with a pre-existing opsgenie
// integration upgrades cleanly and the row is gone, along with the rows
// depending on it — a check_channels link (FK CASCADE) and an
// escalation_policy_targets 'connection' target (no FK, deleted explicitly
// by the migration). A bystander Slack integration and its own links must
// survive untouched.
func TestRemoveOpsgenieMigrationDeletesIntegrationAndDependents(t *testing.T) {
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

	exec(`insert into organizations (uid, slug, name) values ('org-1', 'acme', 'Acme')`)

	exec(`insert into checks (uid, organization_uid, name, slug, type, config)
	      values ('chk-1', 'org-1', 'API health', 'api-health', 'http', '{}')`)

	// The opsgenie integration being removed.
	exec(`insert into integrations (uid, organization_uid, type, name, settings)
	      values ('int-opsgenie', 'org-1', 'opsgenie', 'Old Opsgenie', '{"api_key":"secret"}')`)

	// A bystander Slack integration that must survive untouched.
	exec(`insert into integrations (uid, organization_uid, type, name, settings)
	      values ('int-slack', 'org-1', 'slack', 'Team Slack', '{}')`)

	// check_channels links to BOTH integrations — only the opsgenie one must cascade away.
	exec(`insert into check_channels (uid, organization_uid, check_uid, integration_uid)
	      values ('cc-opsgenie', 'org-1', 'chk-1', 'int-opsgenie')`)
	exec(`insert into check_channels (uid, organization_uid, check_uid, integration_uid)
	      values ('cc-slack', 'org-1', 'chk-1', 'int-slack')`)

	// An escalation policy step that targets the opsgenie connection directly
	// (target_uid is NOT a DB foreign key — the migration must delete this
	// explicitly) plus a bystander 'connection' target on Slack.
	exec(`insert into escalation_policies (uid, organization_uid, name) values ('pol-1', 'org-1', 'Default')`)
	exec(`insert into escalation_policy_steps (uid, policy_uid, position) values ('step-1', 'pol-1', 0)`)
	exec(`insert into escalation_policy_targets (uid, step_uid, target_type, target_uid, position)
	      values ('tgt-opsgenie', 'step-1', 'connection', 'int-opsgenie', 0)`)
	exec(`insert into escalation_policy_targets (uid, step_uid, target_type, target_uid, position)
	      values ('tgt-slack', 'step-1', 'connection', 'int-slack', 1)`)

	// A historical audit row referencing the opsgenie connection — must be
	// kept (audit history), with connection_uid nulled by ON DELETE SET NULL.
	exec(`insert into incidents (uid, organization_uid, check_uid, state, started_at)
	      values ('inc-1', 'org-1', 'chk-1', 1, datetime('now'))`)
	exec(`insert into incident_notifications
	        (uid, organization_uid, incident_uid, event_type, source,
	         connection_uid, channel_type, status, created_at)
	      values ('notif-1', 'org-1', 'inc-1', 'incident.created', 'system',
	              'int-opsgenie', 'opsgenie', 'sent', datetime('now'))`)

	// Re-run the migration (Initialize already ran it against an empty DB).
	_, err = svc.DB().ExecContext(ctx, removeOpsgenieMigrationSQL(t))
	r.NoError(err)

	var count int

	// The opsgenie integration itself is gone.
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from integrations where uid = 'int-opsgenie'`).Scan(&count))
	r.Equal(0, count, "opsgenie integration must be hard-deleted")

	// Its check_channels link cascaded away.
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from check_channels where integration_uid = 'int-opsgenie'`).Scan(&count))
	r.Equal(0, count, "check_channels link to the opsgenie integration must cascade away")

	// Its escalation_policy_targets 'connection' row was deleted explicitly.
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from escalation_policy_targets where target_uid = 'int-opsgenie'`).Scan(&count))
	r.Equal(0, count, "escalation_policy_targets row pointing at the opsgenie integration must be deleted")

	// The audit row survives, with connection_uid nulled.
	var connectionUID *string

	r.NoError(svc.DB().QueryRowContext(ctx,
		`select connection_uid from incident_notifications where uid = 'notif-1'`).Scan(&connectionUID))
	r.Nil(connectionUID, "incident_notifications.connection_uid must be nulled, not the whole row deleted")

	// The bystander Slack integration and its links are untouched.
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from integrations where uid = 'int-slack'`).Scan(&count))
	r.Equal(1, count, "the slack integration must survive")

	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from check_channels where integration_uid = 'int-slack'`).Scan(&count))
	r.Equal(1, count, "the slack check_channels link must survive")

	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from escalation_policy_targets where target_uid = 'int-slack'`).Scan(&count))
	r.Equal(1, count, "the slack escalation_policy_targets row must survive")
}
