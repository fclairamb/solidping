// Package testsupport holds helpers shared by the repository's test suites.
// It is imported only from _test.go files; nothing in the production build
// depends on it.
package testsupport

import (
	"os"
	"strings"
)

// EnvRequirePostgres is the environment variable that turns "embedded Postgres
// would not start" from a silently skipped test into a failed one.
//
// It exists because every Postgres-backed suite in this repository guards
// itself with the same shape:
//
//	svc, err := postgres.New(ctx, &postgres.Config{Embedded: true, ...})
//	if err != nil {
//	    t.Skipf("embedded postgres unavailable: %v", err)
//	}
//
// That is the right behavior on a developer laptop with no Postgres binaries
// downloaded. It is exactly the wrong behavior in CI: a job whose whole
// purpose is to exercise Postgres reports `ok` having proven nothing the moment
// the binary download is blocked, a port clashes, or two packages race over the
// shared ~/.embedded-postgres-go/extracted/pwfile. Spec 2026-09-12-05 exists
// because a Postgres-only regression shipped past a green test suite; a CI job
// built on skip-on-failure would reproduce that bug rather than catch it.
//
// So CI sets SP_TEST_REQUIRE_POSTGRES=1 and every one of those skips becomes a
// hard failure. Local runs leave it unset and keep skipping.
const EnvRequirePostgres = "SP_TEST_REQUIRE_POSTGRES"

// TB is the subset of *testing.T (and *testing.B) these helpers need. It is an
// interface rather than testing.TB so the helpers' own tests can observe what
// they did instead of dying inside the assertion under test — a require-helper
// that has never been seen to fail is the same untrustworthy decoration this
// package exists to remove.
type TB interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// RequirePostgres reports whether the caller has asked for an unusable embedded
// Postgres to fail the run rather than skip it. Anything other than unset, "0",
// "false", "no" or "off" (case-insensitive) means yes.
func RequirePostgres() bool {
	return requirePostgresValue(os.Getenv(EnvRequirePostgres))
}

func requirePostgresValue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// PostgresUnavailable ends the test because an embedded Postgres instance could
// not be started: a hard failure when EnvRequirePostgres is set, a skip
// otherwise. Neither branch returns.
func PostgresUnavailable(t TB, err error) {
	t.Helper()
	postgresUnusable(t, "embedded postgres unavailable", err)
}

// PostgresInitFailed ends the test because an embedded Postgres instance
// started but could not be initialized (migrations, seeding): a hard failure
// when EnvRequirePostgres is set, a skip otherwise. Neither branch returns.
func PostgresInitFailed(t TB, err error) {
	t.Helper()
	postgresUnusable(t, "embedded postgres init failed", err)
}

func postgresUnusable(testingT TB, stage string, err error) {
	testingT.Helper()

	if RequirePostgres() {
		testingT.Fatalf(
			"%s: %v\n"+
				"%s is set, so this is a FAILURE and not a skip: this run was asked to prove the "+
				"Postgres layer actually executes. Fix the embedded Postgres startup (binary "+
				"download, port clash, or the shared ~/.embedded-postgres-go/extracted pwfile race "+
				"-- run with -p 1) rather than unsetting the variable.",
			stage, err, EnvRequirePostgres,
		)

		return
	}

	testingT.Skipf("%s: %v (set %s=1 to make this a failure instead)", stage, err, EnvRequirePostgres)
}
