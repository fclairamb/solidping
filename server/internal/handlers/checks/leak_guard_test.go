package checks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// The leak guard for spec 2026-09-11-03.
//
// The invariant the spec is built on is a NEGATIVE: a value a `${param:}` /
// `${env:}` reference points at must never be findable in the public `config`
// column, whatever the write path did. A negative of that shape is only worth
// anything if something actually looks — so this file greps the real database
// after every test in the package that goes through setupApplyService, and
// carries a positive control proving the grep fires when a value IS there.
//
// The first attempt at this guard (in internal/checkers/registry) did not touch
// a database at all: it planted a reference into a local map and asserted that
// local map did not contain the secret. That is true by construction and could
// never fail. It was deleted rather than patched — a tripwire that cannot trip
// is worse than none, because it reads like coverage.

// leakNeedles are the fixture values this package uses to stand in for "a
// resolved secret". Any of them appearing in a check's PUBLIC config means the
// write path resolved a reference and stored what it resolved.
//
// They are deliberately distinctive strings: a needle like "secret" would trip
// on unrelated config and push someone to weaken the guard.
var leakNeedles = []string{ //nolint:gochecknoglobals // test fixture table
	"hunter2",               // secret_ref_parity_test.go, apply_test.go — the ${param:} value
	"s3cr3t-token",          // apply_test.go — the ${env:} value
	"exposed-fixture-value", // apply_test.go — the no-master-key path
	"org-value",             // apply_test.go — org-scoped parameter
	"system-value",          // apply_test.go — a SYSTEM parameter, which must never resolve at all
	"the-org-own-value",     // secret_ref_parity_test.go — the look-alike key
	"wrapped-dek-material",  // secret_ref_parity_test.go — the org's wrapped DEK
	"js-fixture-password",   // js_secrets_test.go — the js check's `secrets` value
	"js-fixture-env-secret", // js_secrets_test.go — the ${env:} reference's value
	"instance-email.password",
	"instance-msteams.app_secret",
}

// findLeakedSecrets returns one description per (check, needle) pair found in a
// public config, plus the job rows materialized from them. It is a plain
// function rather than an assertion so the positive control below can call it
// and assert it FOUND something.
//
// Only the public halves are scanned: `checks.config` and `check_jobs.config`.
// A secret inside config_private is where a secret belongs.
func findLeakedSecrets(
	ctx context.Context, dbSvc db.Service, orgUID string, needles []string,
) ([]string, error) {
	found := make([]string, 0)

	rows, _, err := dbSvc.ListChecks(ctx, orgUID, &models.ListChecksFilter{})
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		blob, mErr := json.Marshal(row.Config)
		if mErr != nil {
			return nil, mErr
		}

		slug := row.UID
		if row.Slug != nil {
			slug = *row.Slug
		}

		for _, needle := range needles {
			if strings.Contains(string(blob), needle) {
				found = append(found,
					fmt.Sprintf("check %q public config contains %q", slug, needle))
			}
		}
	}

	return found, nil
}

// findLeakedSecretsInJobs is the same grep over the job rows the scheduler
// materializes from a check. A job row is a copy of the check's config, so a
// leak reaches it too — and a job row is what a worker reads.
func findLeakedSecretsInJobs(
	ctx context.Context, dbSvc *sqlite.Service, needles []string,
) ([]string, error) {
	var jobs []models.CheckJob

	if err := dbSvc.DB().NewSelect().Model(&jobs).Scan(ctx); err != nil {
		return nil, err
	}

	found := make([]string, 0)

	for i := range jobs {
		blob, mErr := json.Marshal(jobs[i].Config)
		if mErr != nil {
			return nil, mErr
		}

		for _, needle := range needles {
			if strings.Contains(string(blob), needle) {
				found = append(found,
					fmt.Sprintf("job for check %q carries %q in its public config", jobs[i].CheckUID, needle))
			}
		}
	}

	return found, nil
}

// registerLeakGuard wires the grep into a test's teardown. setupApplyService
// calls it, so every test in this package that writes a check is audited with
// no per-test opt-in — which is the point: the next contributor who reinstates
// `cfg[key] = resolved` finds out from a test they did not write.
//
// Registration order matters. setupApplyService registers the database close
// first; cleanups run LIFO, so registering this one AFTER it means this runs
// BEFORE the database goes away.
func registerLeakGuard(t *testing.T, dbSvc *sqlite.Service, orgUID string) {
	t.Helper()

	t.Cleanup(func() {
		ctx := context.Background()

		leaks, err := findLeakedSecrets(ctx, dbSvc, orgUID, leakNeedles)
		if err != nil {
			return // the database is gone or the test failed earlier; nothing to say
		}

		jobLeaks, err := findLeakedSecretsInJobs(ctx, dbSvc, leakNeedles)
		if err == nil {
			leaks = append(leaks, jobLeaks...)
		}

		for _, leak := range leaks {
			t.Errorf("resolved secret found in a PUBLIC config: %s — "+
				"a ${param:}/${env:} reference must be STORED as the reference and resolved at "+
				"execution (spec 2026-09-11-03)", leak)
		}
	})
}

// TestLeakGuardFiresOnARealLeak is the positive control, modeled on
// TestExportLeakTripwireIsNotVacuous: it writes a check whose public config
// genuinely holds a fixture secret and asserts the grep reports it — for the
// check row and for the job row the scheduler materializes from it.
//
// Without this, deleting the reference-at-rest behavior and breaking the grep
// at the same time would leave every "no leak" assertion in this package green.
//
// It builds its own database rather than using setupApplyService, precisely so
// the guard registered there does not fail this test for the leak it plants on
// purpose.
func TestLeakGuardFiresOnARealLeak(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("leak-control", "Leak Control")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// A clean org first: the grep must be quiet when there is nothing to find,
	// or "it always fires" would satisfy the assertion below.
	clean := models.NewCheck(org.UID, "clean", "http")
	clean.Config = models.JSONMap{"url": "https://example.com", "body": "password=${param:sso}"}
	r.NoError(dbSvc.CreateCheck(ctx, clean))

	quiet, err := findLeakedSecrets(ctx, dbSvc, org.UID, leakNeedles)
	r.NoError(err)
	r.Empty(quiet, "a stored REFERENCE must not read as a leak")

	// Now the leak: exactly what the pre-spec write path did — the resolved
	// value substituted into a non-secret key of the public config.
	leaky := models.NewCheck(org.UID, "leaky", "http")
	leaky.Config = models.JSONMap{"url": "https://example.com", "body": "password=hunter2"}
	r.NoError(dbSvc.CreateCheck(ctx, leaky))

	found, err := findLeakedSecrets(ctx, dbSvc, org.UID, leakNeedles)
	r.NoError(err)
	r.Len(found, 1, "the guard must find a resolved secret in a public config")
	r.Contains(found[0], "leaky")
	r.Contains(found[0], "hunter2")

	// And the job row the scheduler materialized from that check carries it too
	// — which is the copy a worker actually reads.
	jobFound, err := findLeakedSecretsInJobs(ctx, dbSvc, leakNeedles)
	r.NoError(err)
	r.NotEmpty(jobFound, "the guard must scan job rows as well as check rows")
}
