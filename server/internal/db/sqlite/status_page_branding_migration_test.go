package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The three sections spec 2026-08-21-07 contributes to 015_v0_18_0.
const (
	sectionBranding   = "status-page-branding"
	sectionPassword   = "status-page-password"
	sectionSubChannel = "status-subscriber-channels"
)

// migratedSQLite returns a fresh in-memory database with every migration
// applied, plus one organization to hang rows off.
func migratedSQLite(t *testing.T) (context.Context, *Service, string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))

	t.Cleanup(func() { _ = svc.Close() })

	const org = "11111111-1111-1111-1111-111111111111"

	_, err = svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'acme', 'Acme')`, org)
	r.NoError(err)

	return ctx, svc, org
}

// tableColumns returns the column set of a table.
func tableColumns(ctx context.Context, t *testing.T, svc *Service, table string) map[string]bool {
	t.Helper()

	rows, err := svc.DB().QueryContext(ctx, `select name from pragma_table_info(?)`, table)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	out := map[string]bool{}

	for rows.Next() {
		var name string

		require.NoError(t, rows.Scan(&name))

		out[name] = true
	}

	require.NoError(t, rows.Err())
	require.NotEmpty(t, out, "pragma_table_info must have enumerated %s", table)

	return out
}

// TestStatusPageBrandingColumnsPresent proves the branding section ran: a fresh
// database has the three columns and the two partial indexes.
func TestStatusPageBrandingColumnsPresent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := migratedSQLite(t)

	columns := tableColumns(ctx, t, svc, "status_pages")
	r.True(columns["logo_file_uid"])
	r.True(columns["favicon_file_uid"])
	r.True(columns["hide_branding"])
	r.True(columns["password_hash"])
	// Positive control: the pragma really enumerated the table.
	r.True(columns["uid"])

	for _, index := range []string{"status_pages_logo_file_idx", "status_pages_favicon_file_idx"} {
		var count int

		r.NoError(svc.DB().QueryRowContext(ctx,
			`select count(*) from sqlite_master where type = 'index' and name = ?`, index).Scan(&count))
		r.Equal(1, count, "%s must survive the status_pages rebuild in the password section", index)
	}
}

// TestStatusPagePasswordRebuildPreservesRows is the load-bearing one.
//
// The SQLite half of the status-page-password section cannot ALTER the
// `visibility` CHECK constraint, so it rebuilds `status_pages` through the
// `*_new` + copy + drop + rename pattern. That copy is a hand-written column
// list: "it preserves the data" is an ARGUMENT until something executes it
// against a populated table. A column dropped from either half of the
// insert-select, or reordered between them, silently scrambles or discards
// every existing status page — and `insert ... select` is positional, so a
// reorder does not even error.
//
// The section is replayed against an already-migrated database, which is
// exactly what it does on a real upgrade: build the new table, copy, swap.
func TestStatusPagePasswordRebuildPreservesRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := migratedSQLite(t)

	const page = "22222222-2222-2222-2222-222222222222"

	// A row using every column the rebuild has to carry across, with values
	// that are distinguishable from the column defaults — a rebuild that
	// silently dropped a column would otherwise still "pass" by landing on the
	// default.
	_, err := svc.DB().ExecContext(ctx, `
		insert into status_pages (
			uid, organization_uid, name, slug, description, visibility, is_default, enabled,
			show_availability, show_response_time, history_days, language,
			history_period, custom_domain, custom_domain_token, custom_domain_verified_at,
			custom_domain_checked_at, custom_domain_failures, custom_css, settings,
			auto_publish, auto_publish_delay_seconds, auto_resolve,
			logo_file_uid, favicon_file_uid, hide_branding
		) values (
			?, ?, 'Acme Status', 'acme-status', 'All our things', 'private', 1, 0,
			0, 0, 30, 'fr',
			'30d', 'status.acme.com', 'tok3n', '2026-08-01T00:00:00Z',
			'2026-08-02T00:00:00Z', 2, 'body{color:red}', '{"availability":{"thresholdUp":99.5}}',
			1, 120, 'never',
			'logo-file-uid', 'favicon-file-uid', 1
		)`, page, org)
	r.NoError(err)

	// A child row, so the FK-off/on dance around the DROP TABLE is exercised
	// with something that could actually be cascaded away.
	_, err = svc.DB().ExecContext(ctx, `
		insert into status_page_sections (uid, status_page_uid, name, slug, position)
		values ('33333333-3333-3333-3333-333333333333', ?, 'Core', 'core', 0)`, page)
	r.NoError(err)

	execMigrationStatements(ctx, t, svc, migrationSection(t, sectionPassword))

	var got struct {
		Name        string  `bun:"name"`
		Slug        string  `bun:"slug"`
		Description *string `bun:"description"`
		Visibility  string  `bun:"visibility"`
		IsDefault   int     `bun:"is_default"`
		Enabled     int     `bun:"enabled"`
		HistoryDays int     `bun:"history_days"`
		Language    *string `bun:"language"`
		Period      string  `bun:"history_period"`
		Domain      *string `bun:"custom_domain"`
		Token       *string `bun:"custom_domain_token"`
		Failures    int     `bun:"custom_domain_failures"`
		CustomCSS   *string `bun:"custom_css"`
		Settings    string  `bun:"settings"`
		AutoPublish int     `bun:"auto_publish"`
		Delay       int     `bun:"auto_publish_delay_seconds"`
		AutoResolve string  `bun:"auto_resolve"`
		Logo        *string `bun:"logo_file_uid"`
		Favicon     *string `bun:"favicon_file_uid"`
		Hide        int     `bun:"hide_branding"`
	}

	// An explicit column list, not `select *`: bun refuses to scan a column the
	// destination struct has no field for, and naming them here is also what
	// makes the assertions below a checklist of what the rebuild must carry.
	r.NoError(svc.DB().NewRaw(`
		select name, slug, description, visibility, is_default, enabled, history_days, language,
		       history_period, custom_domain, custom_domain_token, custom_domain_failures,
		       custom_css, settings, auto_publish, auto_publish_delay_seconds, auto_resolve,
		       logo_file_uid, favicon_file_uid, hide_branding
		from status_pages where uid = ?`, page).Scan(ctx, &got))

	r.Equal("Acme Status", got.Name)
	r.Equal("acme-status", got.Slug)
	r.Equal("All our things", *got.Description)
	r.Equal("private", got.Visibility)
	r.Equal(1, got.IsDefault)
	r.Equal(0, got.Enabled, "a disabled page must not come back enabled")
	r.Equal(30, got.HistoryDays)
	r.Equal("fr", *got.Language)
	r.Equal("30d", got.Period)
	r.Equal("status.acme.com", *got.Domain)
	r.Equal("tok3n", *got.Token)
	r.Equal(2, got.Failures)
	r.Equal("body{color:red}", *got.CustomCSS)
	r.Contains(got.Settings, "thresholdUp")
	r.Equal(1, got.AutoPublish)
	r.Equal(120, got.Delay)
	r.Equal("never", got.AutoResolve)
	r.Equal("logo-file-uid", *got.Logo)
	r.Equal("favicon-file-uid", *got.Favicon)
	r.Equal(1, got.Hide)

	// The child row survived the parent's DROP TABLE — which it only does
	// because the section turns foreign_keys off around the swap.
	var sections int

	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from status_page_sections where status_page_uid = ?`, page).Scan(&sections))
	r.Equal(1, sections, "the FK pragma dance must not have cascaded the sections away")

	// Foreign keys are back ON: the section must not leave the connection with
	// integrity checking disabled for everything that follows.
	var fkEnabled int

	r.NoError(svc.DB().QueryRowContext(ctx, `pragma foreign_keys`).Scan(&fkEnabled))
	r.Equal(1, fkEnabled, "the section must restore foreign_keys=ON")
}

// TestStatusPageVisibilityCheckAcceptsPassword pins the point of the rebuild:
// the widened CHECK admits the third value, and still refuses anything else.
//
// Without the second half this would pass on a table with NO constraint at all,
// which is a different (and worse) schema than the one intended.
func TestStatusPageVisibilityCheckAcceptsPassword(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := migratedSQLite(t)

	insert := func(uid, slug, visibility string) error {
		_, err := svc.DB().ExecContext(ctx, `
			insert into status_pages (uid, organization_uid, name, slug, visibility)
			values (?, ?, 'P', ?, ?)`, uid, org, slug, visibility)

		return err
	}

	r.NoError(insert("aaaaaaaa-0000-0000-0000-000000000001", "page-public", "public"))
	r.NoError(insert("aaaaaaaa-0000-0000-0000-000000000002", "page-private", "private"))
	r.NoError(insert("aaaaaaaa-0000-0000-0000-000000000003", "page-password", "password"))

	err := insert("aaaaaaaa-0000-0000-0000-000000000004", "page-bogus", "unlisted")
	r.Error(err, "the CHECK must still refuse an unknown visibility")
	r.Contains(err.Error(), "CHECK constraint failed")
}

// TestStatusSubscriberChannelsRebuildPreservesRows is the second rebuild: the
// subscriber table is rebuilt to make `email` nullable, and the same
// positional-copy hazard applies.
//
// The pre-existing row is written with ONLY the columns that exist before the
// section runs, because that is what the section's insert-select carries — the
// new columns are supposed to land on their defaults.
func TestStatusSubscriberChannelsRebuildPreservesRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := migratedSQLite(t)

	const (
		page = "44444444-4444-4444-4444-444444444444"
		sub  = "55555555-5555-5555-5555-555555555555"
	)

	_, err := svc.DB().ExecContext(ctx, `
		insert into status_pages (uid, organization_uid, name, slug) values (?, ?, 'P', 'page-one')`, page, org)
	r.NoError(err)

	_, err = svc.DB().ExecContext(ctx, `
		insert into status_page_subscriber (
			uid, organization_uid, status_page_uid, email, confirmed_at,
			confirm_token, unsubscribe_token, scope
		) values (?, ?, ?, 'subscriber@example.com', '2026-08-01T00:00:00Z', 'ctok', 'utok', 'page')`,
		sub, org, page)
	r.NoError(err)

	execMigrationStatements(ctx, t, svc, migrationSection(t, sectionSubChannel))

	var got struct {
		Email        *string `bun:"email"`
		ConfirmedAt  *string `bun:"confirmed_at"`
		ConfirmToken string  `bun:"confirm_token"`
		UnsubToken   string  `bun:"unsubscribe_token"`
		Scope        string  `bun:"scope"`
		Channel      string  `bun:"channel"`
		FailureCount int     `bun:"failure_count"`
		DisabledAt   *string `bun:"disabled_at"`
	}

	r.NoError(svc.DB().NewRaw(`
		select email, confirmed_at, confirm_token, unsubscribe_token, scope,
		       channel, failure_count, disabled_at
		from status_page_subscriber where uid = ?`, sub).Scan(ctx, &got))

	r.Equal("subscriber@example.com", *got.Email)
	r.NotNil(got.ConfirmedAt, "a confirmed subscriber must not come back unconfirmed")
	r.Equal("ctok", got.ConfirmToken)
	r.Equal("utok", got.UnsubToken)
	r.Equal("page", got.Scope)
	// The new columns land on their defaults — an existing row is an email
	// subscription, live, with no failures.
	r.Equal("email", got.Channel)
	r.Zero(got.FailureCount)
	r.Nil(got.DisabledAt)

	// The token indexes were dropped with the table and must be back, or the
	// confirm/unsubscribe links stop being unique.
	for _, index := range []string{
		"idx_status_page_subscriber_confirm_token",
		"idx_status_page_subscriber_unsub_token",
		"idx_status_page_subscriber_page_confirmed",
		"idx_status_page_subscriber_live",
	} {
		var count int

		r.NoError(svc.DB().QueryRowContext(ctx,
			`select count(*) from sqlite_master where type = 'index' and name = ?`, index).Scan(&count))
		r.Equal(1, count, "%s must be recreated after the rebuild", index)
	}

	var fkEnabled int

	r.NoError(svc.DB().QueryRowContext(ctx, `pragma foreign_keys`).Scan(&fkEnabled))
	r.Equal(1, fkEnabled, "the section must restore foreign_keys=ON")
}

// TestSubscriberLiveIndexIsChannelAware is why the rebuild had to touch the
// index too: several webhook subscriptions on one page all have a NULL email,
// and the pre-existing (page, email, scope, incident) index would have
// collapsed them into one.
func TestSubscriberLiveIndexIsChannelAware(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := migratedSQLite(t)

	const page = "66666666-6666-6666-6666-666666666666"

	_, err := svc.DB().ExecContext(ctx, `
		insert into status_pages (uid, organization_uid, name, slug) values (?, ?, 'P', 'page-one')`, page, org)
	r.NoError(err)

	insertWebhook := func(uid, key string) error {
		_, execErr := svc.DB().ExecContext(ctx, `
			insert into status_page_subscriber (
				uid, organization_uid, status_page_uid, confirm_token, unsubscribe_token,
				scope, channel, endpoint_key
			) values (?, ?, ?, ?, ?, 'page', 'webhook', ?)`,
			uid, org, page, "c-"+uid, "u-"+uid, key)

		return execErr
	}

	r.NoError(insertWebhook("77777777-0000-0000-0000-000000000001", "key-one"))
	r.NoError(insertWebhook("77777777-0000-0000-0000-000000000002", "key-two"),
		"two webhooks on one page must coexist — both have a NULL email")

	// ...and the index still does its job: the SAME endpoint twice is refused.
	err = insertWebhook("77777777-0000-0000-0000-000000000003", "key-one")
	r.Error(err, "a duplicate endpoint on the same page must still be rejected")
	r.Contains(err.Error(), "UNIQUE constraint failed")
}

// TestStatusPageSectionsParity reads the shipped files and pins that each of
// this spec's three sections exists in BOTH directions, in the unreleased
// migration only. A section with no teardown half is silent until somebody
// migrates down.
func TestStatusPageSectionsParity(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, name := range []string{sectionBranding, sectionPassword, sectionSubChannel} {
		up := migrationSection(t, name)
		r.NotEmpty(up, "%s must have an up half", name)

		down := downMigrationSection(t, name)
		r.NotEmpty(down, "%s must have a down half", name)
	}

	// 014_v0_17_0 is RELEASED. A section appended there would never reach a
	// database that already ran it.
	for _, file := range []string{
		"migrations/014_v0_17_0.up.sql",
		"migrations/014_v0_17_0.down.sql",
	} {
		body, err := migrationsFS.ReadFile(file)
		r.NoError(err)

		for _, name := range []string{sectionBranding, sectionPassword, sectionSubChannel} {
			r.NotContains(string(body), "-- SECTION: "+name+"\n",
				"%s is released; %s must not have been appended to it", file, name)
		}
	}

	// Positive control for the NotContains above: the banners ARE in 015.
	for _, file := range []string{
		"migrations/015_v0_18_0.up.sql",
		"migrations/015_v0_18_0.down.sql",
	} {
		body, err := migrationsFS.ReadFile(file)
		r.NoError(err)

		for _, name := range []string{sectionBranding, sectionPassword, sectionSubChannel} {
			r.Contains(string(body), "-- SECTION: "+name+"\n", "%s must carry %s", file, name)
		}
	}
}
