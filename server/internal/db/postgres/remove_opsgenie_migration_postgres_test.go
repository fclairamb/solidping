package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// portRemoveOpsgenieMigration is distinct from every other _postgres_test.go
// embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portRemoveOpsgenieMigration = 15479

// TestRemoveOpsgenieMigrationDeletesIntegrationAndDependents_Postgres is the
// Postgres twin of the SQLite migration test: spec 2026-08-19-02's data
// migration removes an org's pre-existing opsgenie integration cleanly,
// cascading its check_channels link (DB-enforced FK) and its
// escalation_policy_targets 'connection' target (NOT DB-enforced — the
// migration must delete it explicitly, since target_uid is a polymorphic
// column shared by every target_type). A bystander Slack integration and its
// own links must survive untouched, and the incident_notifications audit
// row survives with connection_uid nulled.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestRemoveOpsgenieMigrationDeletesIntegrationAndDependents_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portRemoveOpsgenieMigration, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	migration, err := migrationsFS.ReadFile("migrations/015_remove_opsgenie_integrations.up.sql")
	r.NoError(err)

	exec := func(query string, args ...any) {
		t.Helper()

		_, execErr := svc.DB().ExecContext(ctx, query, args...)
		r.NoError(execErr)
	}

	org := "11111111-1111-1111-1111-111111111111"
	chk := "22222222-2222-2222-2222-222222222222"
	intOpsgenie := "33333333-3333-3333-3333-333333333333"
	intSlack := "44444444-4444-4444-4444-444444444444"
	policy := "55555555-5555-5555-5555-555555555555"
	step := "66666666-6666-6666-6666-666666666666"
	incident := "77777777-7777-7777-7777-777777777777"

	exec(`insert into organizations (uid, slug, name) values (?, 'acme', 'Acme')`, org)
	exec(`insert into checks (uid, organization_uid, name, slug, type, config)
	      values (?, ?, 'API health', 'api-health', 'http', '{}')`, chk, org)

	exec(`insert into integrations (uid, organization_uid, type, name, settings)
	      values (?, ?, 'opsgenie', 'Old Opsgenie', '{"api_key":"secret"}')`, intOpsgenie, org)
	exec(`insert into integrations (uid, organization_uid, type, name, settings)
	      values (?, ?, 'slack', 'Team Slack', '{}')`, intSlack, org)

	exec(`insert into check_channels (uid, organization_uid, check_uid, integration_uid)
	      values (gen_random_uuid(), ?, ?, ?)`, org, chk, intOpsgenie)
	exec(`insert into check_channels (uid, organization_uid, check_uid, integration_uid)
	      values (gen_random_uuid(), ?, ?, ?)`, org, chk, intSlack)

	exec(`insert into escalation_policies (uid, organization_uid, name) values (?, ?, 'Default')`, policy, org)
	exec(`insert into escalation_policy_steps (uid, policy_uid, position) values (?, ?, 0)`, step, policy)
	exec(`insert into escalation_policy_targets (uid, step_uid, target_type, target_uid, position)
	      values (gen_random_uuid(), ?, 'connection', ?, 0)`, step, intOpsgenie)
	exec(`insert into escalation_policy_targets (uid, step_uid, target_type, target_uid, position)
	      values (gen_random_uuid(), ?, 'connection', ?, 1)`, step, intSlack)

	exec(`insert into incidents (uid, organization_uid, check_uid, state, started_at)
	      values (?, ?, ?, 1, now())`, incident, org, chk)
	exec(`insert into incident_notifications
	        (uid, organization_uid, incident_uid, event_type, source, connection_uid, channel_type, status)
	      values (gen_random_uuid(), ?, ?, 'incident.created', 'system', ?, 'opsgenie', 'sent')`,
		org, incident, intOpsgenie)

	// The migration itself.
	_, err = svc.DB().ExecContext(ctx, string(migration))
	r.NoError(err)

	count := func(query string, args ...any) int {
		t.Helper()

		var out int
		r.NoError(svc.DB().QueryRowContext(ctx, query, args...).Scan(&out))

		return out
	}

	r.Equal(0, count(`select count(*) from integrations where uid = ?`, intOpsgenie),
		"opsgenie integration must be hard-deleted")
	r.Equal(0, count(`select count(*) from check_channels where integration_uid = ?`, intOpsgenie),
		"check_channels link to the opsgenie integration must cascade away")
	r.Equal(0, count(`select count(*) from escalation_policy_targets where target_uid = ?`, intOpsgenie),
		"escalation_policy_targets row pointing at the opsgenie integration must be deleted")

	var connectionUID *string

	r.NoError(svc.DB().QueryRowContext(ctx,
		`select connection_uid from incident_notifications where incident_uid = ?`, incident).Scan(&connectionUID))
	r.Nil(connectionUID, "incident_notifications.connection_uid must be nulled, not the whole row deleted")

	r.Equal(1, count(`select count(*) from integrations where uid = ?`, intSlack),
		"the slack integration must survive")
	r.Equal(1, count(`select count(*) from check_channels where integration_uid = ?`, intSlack),
		"the slack check_channels link must survive")
	r.Equal(1, count(`select count(*) from escalation_policy_targets where target_uid = ?`, intSlack),
		"the slack escalation_policy_targets row must survive")

	// Idempotent — re-running against an already-clean database is a no-op.
	_, err = svc.DB().ExecContext(ctx, string(migration))
	r.NoError(err)
}
