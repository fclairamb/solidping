package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Embedded-postgres ports for this spec's migration tests. Each test owns one,
// so siblings can run without fighting over a cluster.
const (
	portStatusPageBranding = 15486
	portStatusPageRollback = 15487
)

// subscriberEndpointColumns are the columns the status-subscriber-channels
// section adds. Named once so the apply and rollback tests cannot drift apart
// on what "the new columns" means.
//
//nolint:gochecknoglobals // shared test fixture, read-only
var subscriberEndpointColumns = []string{
	"channel", "endpoint_private", "endpoint_hint", "endpoint_key", "failure_count", "disabled_at",
}

// The three sections spec 2026-08-21-07 contributes to 015_v0_18_0.
const (
	sectionBrandingPG   = "status-page-branding"
	sectionPasswordPG   = "status-page-password"
	sectionSubChannelPG = "status-subscriber-channels"
)

// execPGMigrationStatements runs one migration block statement by statement,
// splitting on a lone `--bun:split` LINE exactly as bun's migrator does.
func execPGMigrationStatements(ctx context.Context, t *testing.T, svc *Service, block string) {
	t.Helper()

	for _, stmt := range pgBunSplitRE.Split(block, -1) {
		if !hasSQL(stmt) {
			continue
		}

		_, err := svc.DB().ExecContext(ctx, stmt)
		require.NoError(t, err, "statement failed:\n%s", stmt)
	}
}

// TestStatusPageBrandingMigration_Postgres pins the Postgres half of all three
// sections against a real cluster: the resulting schema, the behavior the new
// constraints and index encode, and that REPLAYING the sections against a
// populated database changes nothing.
//
// The replay matters for the same reason the SQLite rebuild test does, from the
// other direction: Postgres does not rebuild the tables, so the risk here is
// not data loss but a section that cannot be re-run at all — which is how an
// interrupted migration turns into a cluster nobody can move forward.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestStatusPageBrandingMigration_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portStatusPageBranding, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	count := func(query string, args ...any) int {
		t.Helper()

		var out int
		r.NoError(svc.DB().QueryRowContext(ctx, query, args...).Scan(&out))

		return out
	}

	column := func(table, name string) int {
		t.Helper()

		return count(`select count(*) from information_schema.columns
		              where table_name = ? and column_name = ?`, table, name)
	}

	// --- Schema landed. ---
	for _, name := range []string{"logo_file_uid", "favicon_file_uid", "hide_branding", "password_hash"} {
		r.Equal(1, column("status_pages", name), "status_pages.%s must exist", name)
	}

	for _, name := range subscriberEndpointColumns {
		r.Equal(1, column("status_page_subscriber", name), "status_page_subscriber.%s must exist", name)
	}

	// `email` must be NULLABLE now — a webhook subscriber has none, and the
	// whole subscriber insert would fail on a NOT NULL left in place.
	r.Equal(1, count(`select count(*) from information_schema.columns
	                  where table_name = 'status_page_subscriber'
	                    and column_name = 'email' and is_nullable = 'YES'`))

	// The two brand-asset indexes are PARTIAL: almost every row has NULL there.
	for _, index := range []string{"status_pages_logo_file_idx", "status_pages_favicon_file_idx"} {
		var def string

		r.NoError(svc.DB().QueryRowContext(ctx,
			`select indexdef from pg_indexes where indexname = ?`, index).Scan(&def))
		r.Contains(def, "WHERE", "%s must be partial", index)
	}

	// --- Behavior: the widened visibility CHECK. ---
	org := "11111111-1111-1111-1111-111111111111"
	_, err = svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'acmebrand', 'Acme Brand')`, org)
	r.NoError(err)

	insertPage := func(uid, slug, visibility string) error {
		_, execErr := svc.DB().ExecContext(ctx, `
			insert into status_pages (uid, organization_uid, name, slug, visibility)
			values (?, ?, 'P', ?, ?)`, uid, org, slug, visibility)

		return execErr
	}

	r.NoError(insertPage("22222222-0000-0000-0000-000000000001", "page-public", "public"))
	r.NoError(insertPage("22222222-0000-0000-0000-000000000002", "page-private", "private"))
	r.NoError(insertPage("22222222-0000-0000-0000-000000000003", "page-password", "password"))
	r.Error(insertPage("22222222-0000-0000-0000-000000000004", "page-bogus", "unlisted"),
		"the CHECK must still refuse an unknown visibility")

	// --- Behavior: the channel-aware live-uniqueness index. ---
	page := "22222222-0000-0000-0000-000000000001"

	insertWebhook := func(uid, key string) error {
		_, execErr := svc.DB().ExecContext(ctx, `
			insert into status_page_subscriber (
				uid, organization_uid, status_page_uid, confirm_token, unsubscribe_token,
				scope, channel, endpoint_key
			) values (?, ?, ?, ?, ?, 'page', 'webhook', ?)`,
			uid, org, page, "c-"+uid, "u-"+uid, key)

		return execErr
	}

	r.NoError(insertWebhook("33333333-0000-0000-0000-000000000001", "key-one"))
	r.NoError(insertWebhook("33333333-0000-0000-0000-000000000002", "key-two"),
		"two webhooks on one page must coexist — both have a NULL email")
	r.Error(insertWebhook("33333333-0000-0000-0000-000000000003", "key-one"),
		"a duplicate endpoint on the same page must still be rejected")

	// The channel CHECK is real.
	_, err = svc.DB().ExecContext(ctx, `
		insert into status_page_subscriber (
			uid, organization_uid, status_page_uid, confirm_token, unsubscribe_token, scope, channel
		) values ('33333333-0000-0000-0000-000000000009', ?, ?, 'cx', 'ux', 'page', 'carrier-pigeon')`,
		org, page)
	r.Error(err, "an unknown channel must be refused")

	// An ordinary email subscriber, to survive the replay below.
	_, err = svc.DB().ExecContext(ctx, `
		insert into status_page_subscriber (
			uid, organization_uid, status_page_uid, email, confirm_token, unsubscribe_token, scope
		) values ('44444444-0000-0000-0000-000000000001', ?, ?, 'keeper@example.com', 'ck', 'uk', 'page')`,
		org, page)
	r.NoError(err)

	// --- Replay: every section must be re-runnable, and change nothing. ---
	for _, name := range []string{sectionBrandingPG, sectionPasswordPG, sectionSubChannelPG} {
		execPGMigrationStatements(ctx, t, svc, migrationSection(t, name))
	}

	r.Equal(3, count(`select count(*) from status_pages where organization_uid = ?`, org),
		"replaying the sections must not have dropped a status page")
	r.Equal(3, count(`select count(*) from status_page_subscriber where organization_uid = ?`, org),
		"replaying the sections must not have dropped a subscriber")
	r.Equal(1, count(`select count(*) from status_page_subscriber
	                  where organization_uid = ? and email = 'keeper@example.com'`, org))

	// ...and the constraints still hold afterwards, which is the half a
	// "nothing errored" replay check would miss: a section that dropped a
	// constraint and failed to re-add it looks like a clean replay.
	r.Error(insertPage("22222222-0000-0000-0000-000000000005", "page-bogus2", "unlisted"))
	r.Error(insertWebhook("33333333-0000-0000-0000-000000000004", "key-one"))
}

// TestStatusPageBrandingRollback_Postgres EXECUTES the three teardown halves
// against a migrated, populated database.
//
// Reading a `.down.sql` as text proves the statements were written, not that
// they run — a `drop column` naming a column that does not exist, or sections
// unwound in the wrong order, is silent until somebody actually migrates down,
// and by then the symptom is a half-migrated schema.
//
// The load-bearing assertions are the last ones: the tables themselves must
// survive with their original rows. A teardown that took `status_pages` with it
// would satisfy "the columns are gone" just as well.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestStatusPageBrandingRollback_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portStatusPageRollback, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	count := func(query string, args ...any) int {
		t.Helper()

		var out int
		r.NoError(svc.DB().QueryRowContext(ctx, query, args...).Scan(&out))

		return out
	}

	column := func(table, name string) int {
		t.Helper()

		return count(`select count(*) from information_schema.columns
		              where table_name = ? and column_name = ?`, table, name)
	}

	org := "55555555-5555-5555-5555-555555555555"
	_, err = svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'acmeroll2', 'Acme Rollback 2')`, org)
	r.NoError(err)

	// A password page and a webhook subscriber — precisely the rows the down
	// halves document as lossy, so the "safe direction" claims get executed.
	page := "66666666-0000-0000-0000-000000000001"
	_, err = svc.DB().ExecContext(ctx, `
		insert into status_pages (uid, organization_uid, name, slug, visibility, password_hash, hide_branding)
		values (?, ?, 'Locked', 'locked-page', 'password', '$argon2id$x', true)`, page, org)
	r.NoError(err)

	_, err = svc.DB().ExecContext(ctx, `
		insert into status_page_subscriber (
			uid, organization_uid, status_page_uid, email, confirm_token, unsubscribe_token, scope
		) values ('77777777-0000-0000-0000-000000000001', ?, ?, 'keeper@example.com', 'ck2', 'uk2', 'page')`,
		org, page)
	r.NoError(err)

	_, err = svc.DB().ExecContext(ctx, `
		insert into status_page_subscriber (
			uid, organization_uid, status_page_uid, confirm_token, unsubscribe_token,
			scope, channel, endpoint_key
		) values ('77777777-0000-0000-0000-000000000002', ?, ?, 'cw', 'uw', 'page', 'webhook', 'k')`,
		org, page)
	r.NoError(err)

	// Applied.
	r.Equal(1, column("status_pages", "password_hash"))
	r.Equal(1, column("status_page_subscriber", "channel"))

	// Unwind in the SAME order the .down.sql lists them (reverse of the up).
	for _, name := range []string{sectionSubChannelPG, sectionPasswordPG, sectionBrandingPG} {
		execPGMigrationStatements(ctx, t, svc, downMigrationSection(t, name))
	}

	// Every added column is gone.
	for _, name := range []string{"logo_file_uid", "favicon_file_uid", "hide_branding", "password_hash"} {
		r.Zero(column("status_pages", name), "status_pages.%s must be dropped", name)
	}

	for _, name := range subscriberEndpointColumns {
		r.Zero(column("status_page_subscriber", name), "status_page_subscriber.%s must be dropped", name)
	}

	r.Zero(count(`select count(*) from pg_indexes where indexname = 'status_pages_logo_file_idx'`))

	// The documented lossy outcomes, executed rather than asserted in prose:
	// the password page is back to `private` (never `public` — a page shared
	// behind a secret must not be published by a rollback), and the webhook
	// subscription is deleted while the email one survives.
	r.Equal(1, count(`select count(*) from status_pages where uid = ? and visibility = 'private'`, page))
	r.Equal(1, count(`select count(*) from status_page_subscriber where organization_uid = ?`, org))
	r.Equal(1, count(`select count(*) from status_page_subscriber
	                  where organization_uid = ? and email = 'keeper@example.com'`, org))

	// The tables themselves survived with their identity intact.
	r.Equal(1, column("status_pages", "uid"))
	r.Equal(1, column("status_page_subscriber", "uid"))
	r.Equal(1, count(`select count(*) from information_schema.columns
	                  where table_name = 'status_page_subscriber'
	                    and column_name = 'email' and is_nullable = 'NO'`),
		"the NOT NULL on email must be restored")
}
