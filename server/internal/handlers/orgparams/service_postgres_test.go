package orgparams_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/orgparams"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// This file exists because spec 2026-09-11-03 shipped a feature that was
// non-functional on Postgres and passed every test.
//
// `parameters.key` has carried `check (key ~ '^[a-z0-9_\.]+$')` on Postgres
// since the released 001 baseline — no hyphen. SQLite's `parameters` had no
// CHECK at all. The spec's key rule allows hyphens and every documented example
// uses one, so `PUT /orgs/:org/parameters/sso-authtest-password` answered 500
// with a raw SQLSTATE 23514 on the engine production runs, while the entire
// SQLite-only test suite (and three review rounds) stayed green.
//
// Port 15530: distinct from every other _postgres_test.go port claimed in the
// repo (15438-15522 are taken — see the port-numbering comment in
// postgres_headroom_postgres_test.go).
const portOrgParamsPG = 15530

// The documented example key, verbatim from the spec, wiki/features/config-as-code.md,
// the docs site and the dashboard dialog's placeholder. It is the exact string a
// user following the documentation types first, so it is the exact string a test
// has to cover.
const documentedKey = "sso-authtest-password"

// ONE embedded Postgres for the whole package, booted once and shared.
//
// The repo's usual pattern is a port constant per suite function, and two
// earlier drafts of this file followed it — first with one port for three tests,
// then with a port each. Both SILENTLY SKIPPED the round-trip test, which is the
// deliverable: embedded-postgres keeps its extracted binaries and its `pwfile`
// in one shared directory (~/.embedded-postgres-go/extracted), so two instances
// starting concurrently in the same package race over it and the loser reports
// "unable to remove password file". `t.Skipf` then swallowed it and the file read
// green while proving nothing — the same shape as the bug it exists for.
//
// Sharing one instance removes the race instead of timing around it, and boots
// Postgres once instead of twice. Each test creates its own organization, so they
// stay independent under t.Parallel.
//
//nolint:gochecknoglobals // one process-wide fixture, torn down in TestMain
var (
	pgOnce     sync.Once
	pgSvc      *postgres.Service
	errStartPG error
)

// TestMain closes the shared instance after the last test. It cannot be a
// t.Cleanup: the instance outlives every individual test.
func TestMain(m *testing.M) {
	code := m.Run()

	if pgSvc != nil {
		_ = pgSvc.Close()
	}

	os.Exit(code)
}

// newPostgresOrg returns the shared embedded Postgres plus a fresh organization,
// self-skipping under -short (the default `make test` mode) and, when embedded
// Postgres genuinely cannot start, deferring to testsupport.PostgresUnavailable
// — a skip locally, a hard failure under SP_TEST_REQUIRE_POSTGRES=1.
//
// Worth knowing before trusting a green run here: `make test` passes `-short`,
// so this file SKIPS there. The non-short layer runs in the `backend-postgres`
// CI job and locally via `make test-postgres` (wiki/testing/test-layers.md).
func newPostgresOrg(t *testing.T, slug string) (*postgres.Service, *models.Organization) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	pgOnce.Do(func() {
		ctx := context.Background()

		svc, err := postgres.New(ctx, &postgres.Config{
			Embedded: true, Port: portOrgParamsPG, RunMode: "test",
		})
		if err != nil {
			errStartPG = err

			return
		}

		if initErr := svc.Initialize(ctx); initErr != nil {
			errStartPG = initErr
			_ = svc.Close()

			return
		}

		pgSvc = svc
	})

	if errStartPG != nil || pgSvc == nil {
		testsupport.PostgresUnavailable(t, errStartPG)
	}

	r := require.New(t)
	org := models.NewOrganization(slug, "Org Params PG")
	r.NoError(pgSvc.CreateOrganization(t.Context(), org))

	return pgSvc, org
}

// assertHyphenatedKeyRoundTrips is the whole point, run against a given engine:
// create, get, list, delete a key that contains hyphens — through the SERVICE,
// so the `usr.` storage prefix is applied exactly as the API applies it.
func assertHyphenatedKeyRoundTrips(
	ctx context.Context, t *testing.T, dbSvc db.Service, orgSlug string,
) {
	t.Helper()
	r := require.New(t)

	svc := orgparams.NewService(dbSvc)

	// CREATE — this is the call that returned 500 on Postgres.
	created, err := svc.Set(ctx, orgSlug, documentedKey,
		&orgparams.SetRequest{Value: "hunter2", Secret: boolPtr(true)})
	r.NoError(err, "the documented example key must be storable")
	r.Equal(documentedKey, created.Key)
	r.True(created.Secret)
	r.Nil(created.Value, "a secret parameter is write-only")

	// A second hyphenated key, and a non-secret one, so the list below has
	// something to order and something whose value is legitimately returned.
	_, err = svc.Set(ctx, orgSlug, "acme-region-label",
		&orgparams.SetRequest{Value: "paris", Secret: boolPtr(false)})
	r.NoError(err)

	// GET
	got, err := svc.Get(ctx, orgSlug, documentedKey)
	r.NoError(err)
	r.Equal(documentedKey, got.Key)
	r.Nil(got.Value)

	// LIST — ordered by the STORAGE key ascending (`usr.acme-region-label`
	// before `usr.sso-authtest-password`), with the prefix stripped. Ordering is
	// asserted here rather than only on SQLite because ListOrgParameters was
	// added to both engines and `order by key asc` is exactly the kind of thing
	// that can differ (collation) between them.
	list, err := svc.List(ctx, orgSlug)
	r.NoError(err)
	r.Len(list.Data, 2)
	r.Equal("acme-region-label", list.Data[0].Key)
	r.Equal(documentedKey, list.Data[1].Key)
	r.NotNil(list.Data[0].Value)
	r.Equal("paris", *list.Data[0].Value)
	r.Nil(list.Data[1].Value, "the list must never carry a secret value")

	// ROTATE — same key, new value, no new row.
	_, err = svc.Set(ctx, orgSlug, documentedKey, &orgparams.SetRequest{Value: "rotated", Secret: boolPtr(true)})
	r.NoError(err)

	list, err = svc.List(ctx, orgSlug)
	r.NoError(err)
	r.Len(list.Data, 2, "rotation must not create a second row")

	// DELETE, and the soft-deleted row must drop out of the list — the
	// `deleted_at is null` filter, on this engine.
	r.NoError(svc.Delete(ctx, orgSlug, documentedKey))

	list, err = svc.List(ctx, orgSlug)
	r.NoError(err)
	r.Len(list.Data, 1, "a soft-deleted parameter must not be listed")
	r.Equal("acme-region-label", list.Data[0].Key)

	_, err = svc.Get(ctx, orgSlug, documentedKey)
	r.ErrorIs(err, orgparams.ErrNotFound)

	// And the key is re-creatable after deletion: the partial unique index is on
	// live rows only, so a soft-deleted row must not block it.
	_, err = svc.Set(ctx, orgSlug, documentedKey, &orgparams.SetRequest{Value: "again", Secret: boolPtr(true)})
	r.NoError(err, "a deleted key must be re-creatable")
}

func boolPtr(v bool) *bool { return &v }

// TestOrgParametersRoundTripOnPostgres is the deliverable: the feature, on the
// engine production runs.
func TestOrgParametersRoundTripOnPostgres(t *testing.T) {
	t.Parallel()

	dbSvc, org := newPostgresOrg(t, "orgparams-rt")
	assertHyphenatedKeyRoundTrips(t.Context(), t, dbSvc, org.Slug)
}

// TestOrgParametersRoundTripOnSQLite runs the IDENTICAL assertions on SQLite, so
// the pair is what proves parity rather than two tests that happen to agree.
// Both engines now carry the same CHECK on parameters.key.
func TestOrgParametersRoundTripOnSQLite(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("orgparams-sqlite", "Org Params SQLite")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	assertHyphenatedKeyRoundTrips(ctx, t, dbSvc, org.Slug)
}

// badKeys are shapes the `parameters.key` CHECK exists to refuse. They matter
// beyond the org API: system parameters share this table, and a key with a
// space or a slash in it is not something any code path should be able to write.
//
// Every one of these is spelled with the `usr.` prefix already applied, because
// that is what reaches the database — the point is the CONSTRAINT, not
// paramkeys.Validate (which is covered in internal/paramkeys).
//
//nolint:gochecknoglobals // test fixture table
var badKeys = []string{
	"usr.Uppercase",
	"usr.has space",
	"usr.has/slash",
	"usr.has:colon",
	"usr.has'quote",
	"usr.has%percent",
	"",
}

// assertConstraintStillRefusesJunk proves the widening did not turn the CHECK
// into a rubber stamp. Written against the raw store, not the service, so it is
// the DATABASE refusing — paramkeys.Validate would have rejected these long
// before a real request reached here.
func assertConstraintStillRefusesJunk(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID string) {
	t.Helper()
	r := require.New(t)

	for _, key := range badKeys {
		err := dbSvc.SetOrgParameter(ctx, orgUID, key, "value", false)
		r.Errorf(err, "the parameters.key CHECK must still refuse %q", key)
	}

	// The positive control: the hyphenated key the widening was FOR is accepted
	// by the same store, so the loop above cannot be passing because every write
	// fails.
	r.NoError(dbSvc.SetOrgParameter(ctx, orgUID, paramkeys.StorageKey(documentedKey), "value", true),
		"a hyphenated key must be accepted — that is the whole fix")
}

// TestParameterKeyConstraintOnPostgres pins what the widened CHECK still
// refuses, and — in the same shared instance — that it still accepts every key
// SolidPing itself writes.
func TestParameterKeyConstraintOnPostgres(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dbSvc, org := newPostgresOrg(t, "orgparams-ck")
	ctx := t.Context()

	assertConstraintStillRefusesJunk(ctx, t, dbSvc, org.UID)

	// The other direction a migration can break silently: the widened CHECK
	// must still accept every key SolidPing itself writes, or a deployment
	// would start failing on its own encryption DEK.
	assertPlatformKeysAreWritable(ctx, r, dbSvc, org.UID)
}

// platformParameterKeys are the keys SolidPing itself writes to this table.
// Every one must survive the widened CHECK on both engines.
//
//nolint:gochecknoglobals // test fixture table
var platformParameterKeys = []string{
	"encryption.dek",
	"default_regions",
	"custom_regions",
	"demo.enabled",
	"samples.loaded",
	"registration.email_pattern",
	"registration.slack_workspace_auto_join",
	"auth.session.max_duration",
	"diagnostics.traceroute.enabled",
	"status_page.publication_notify_cap",
	"msteams.app_secret",
	"posthog.personal_api_key",
	"telegram.webhook_secret",
	"operator_notifications",
	"platform_watchdog",
}

func assertPlatformKeysAreWritable(
	ctx context.Context, r *require.Assertions, dbSvc db.Service, orgUID string,
) {
	for _, key := range platformParameterKeys {
		r.NoErrorf(dbSvc.SetOrgParameter(ctx, orgUID, key, "v", true),
			"platform key %q must still be writable", key)
		r.NoErrorf(dbSvc.SetSystemParameter(ctx, key, "v", true),
			"platform system key %q must still be writable", key)
	}
}

// TestPlatformParameterKeysSurviveOnSQLite is the parity half of the assertion
// folded into TestParameterKeyConstraintOnPostgres above — and the one that
// matters most here, because SQLite gained this CHECK for the first time in
// 021_v0_28_0. A rule that rejected `encryption.dek` would make every credential
// in a SQLite deployment unrecoverable.
func TestPlatformParameterKeysSurviveOnSQLite(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("orgparams-plat", "Org Params Platform")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	assertPlatformKeysAreWritable(ctx, r, dbSvc, org.UID)
}
