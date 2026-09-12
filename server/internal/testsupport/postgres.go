// Package testsupport holds helpers shared by the repository's test suites.
// It is imported only from _test.go files; nothing in the production build
// depends on it.
package testsupport

import (
	"fmt"
	"io"
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
		testingT.Fatalf("%s", requiredMessage(stage, err))

		return
	}

	testingT.Skipf("%s", skippedMessage(stage, err))
}

// requiredMessage and skippedMessage are shared by the TB helpers and by
// PostgresUnavailableTestMain so the two paths cannot drift apart.
func requiredMessage(stage string, err error) string {
	return fmt.Sprintf(
		"%s: %v\n"+
			"%s is set, so this is a FAILURE and not a skip: this run was asked to prove the "+
			"Postgres layer actually executes. Fix the embedded Postgres startup (binary "+
			"download, port clash, or the shared ~/.embedded-postgres-go/extracted pwfile race "+
			"-- run with -p 1) rather than unsetting the variable.",
		stage, err, EnvRequirePostgres,
	)
}

func skippedMessage(stage string, err error) string {
	return fmt.Sprintf("%s: %v (set %s=1 to make this a failure instead)", stage, err, EnvRequirePostgres)
}

// PostgresUnavailableTestMain is the TestMain-shaped counterpart of
// PostgresUnavailable. A TestMain has no *testing.T, so it cannot reach
// t.Fatalf; it decides for the whole binary instead.
//
// It writes the explanation to out and reports whether the caller must abort
// the test binary with a non-zero status. A false return means the caller
// should keep whatever skip-everything behavior it had — the developer-laptop
// default.
//
// The one caller today is test/integration/scenario, whose TestMain boots a
// shared embedded Postgres and, on failure, used to run the suite anyway so
// that every scenario self-skipped. That is the same hole as a bare t.Skipf,
// worded differently, and it is inside the backend-postgres job's ./... scope.
func PostgresUnavailableTestMain(out io.Writer, err error) bool {
	required := RequirePostgres()

	if required {
		_, _ = fmt.Fprintf(out, "%s\n", requiredMessage("embedded postgres unavailable", err))

		return true
	}

	_, _ = fmt.Fprintf(out, "%s\n", skippedMessage("embedded postgres unavailable", err))

	return false
}
