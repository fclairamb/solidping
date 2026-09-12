package checkworker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// This file covers the execution half of spec 2026-09-11-03: a check config
// STORES `${param:KEY}` / `${env:NAME}` and the value is materialized only on
// the way into the checker. Nothing here asserts a string equality that a
// redaction layer could fake — every assertion reads either what the checker
// actually received, or what is actually on disk.

// seedRefCheck creates a check whose public config carries a reference, and
// makes its job due. No envelope: the point is that a reference in a
// NON-secret key (the `body` an HTTP form post carries) is the case the old
// apply-time resolution leaked, because `body` is not in SecretFields().
//
//nolint:revive // Test helper, context parameter order matches the package convention.
func seedRefCheck(
	t *testing.T, ctx context.Context, dbSvc *sqlite.Service,
	orgUID, slug, checkType string, config models.JSONMap,
) *models.Check {
	t.Helper()

	check := models.NewCheck(orgUID, slug, checkType)
	check.Config = config
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	due := time.Now().Add(-time.Second)

	_, err := dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("config = ?", config).
		Set("scheduled_at = ?", due).
		Set("effective_scheduled_at = ?", due).
		Where("check_uid = ?", check.UID).
		Exec(ctx)
	require.NoError(t, err)

	return check
}

// assertStoredConfigHoldsTheReference re-reads the check AND its job row from
// the database and asserts both still carry the reference — never the value.
// This is the invariant the spec calls structural: nothing a read endpoint
// serves, and nothing at rest, may hold the resolved value.
//
//nolint:revive // Test helper, context parameter order matches the package convention.
func assertStoredConfigHoldsTheReference(
	t *testing.T, ctx context.Context, dbSvc *sqlite.Service,
	orgUID, checkUID, key, reference, value string,
) {
	t.Helper()
	r := require.New(t)

	check, err := dbSvc.GetCheck(ctx, orgUID, checkUID)
	r.NoError(err)
	r.Equal(reference, check.Config[key], "the stored check config must hold the reference")

	blob, err := json.Marshal(check.Config)
	r.NoError(err)
	r.NotContains(string(blob), value, "no resolved value may be stored on the check")

	var jobs []models.CheckJob

	r.NoError(dbSvc.DB().NewSelect().Model(&jobs).Where("check_uid = ?", checkUID).Scan(ctx))
	r.NotEmpty(jobs)

	for i := range jobs {
		jobBlob, mErr := json.Marshal(jobs[i].Config)
		r.NoError(mErr)
		r.NotContains(string(jobBlob), value, "no resolved value may be stored on the job row")
	}
}

// TestWorkerResolvesParamReferenceAtExecution is the in-process end-to-end
// proof: the org parameter is created through the store the API writes, the
// check stores only the reference, and the checker receives the value.
//
//nolint:paralleltest // Test uses shared database state
func TestWorkerResolvesParamReferenceAtExecution(t *testing.T) {
	r := require.New(t)
	rec := &configRecorder{}
	runner, dbSvc, ctx := setupSecretRunner(t, nil, rec)

	org := models.NewOrganization("param-ref-e2e", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, "sso-password", "hunter2", true))

	const reference = "grant_type=password&password=${param:sso-password}"

	check := seedRefCheck(t, ctx, dbSvc, org.UID, "param-ref-e2e", string(secretCheckType),
		models.JSONMap{"url": "https://x.test", "body": reference})

	jobs, _, err := runner.backend.ClaimJobs(ctx, runner.getWorker().UID, nil, 10, 10, time.Minute)
	r.NoError(err)
	r.Len(jobs, 1)
	r.NoError(runner.executeJob(ctx, runner.logger, jobs[0]))

	r.Equal("grant_type=password&password=hunter2", rec.get("body"),
		"the checker must receive the resolved value")

	assertStoredConfigHoldsTheReference(t, ctx, dbSvc, org.UID, check.UID, "body", reference, "hunter2")
}

// TestWorkerResolvesEnvReferenceOnTheExecutingProcess pins the documented
// difference between the two schemes: `env:` is read from the environment of
// whichever process runs the check — here, the worker's.
//
// t.Setenv forbids t.Parallel, and the package's tests share database state.
func TestWorkerResolvesEnvReferenceOnTheExecutingProcess(t *testing.T) {
	r := require.New(t)
	rec := &configRecorder{}
	runner, dbSvc, ctx := setupSecretRunner(t, nil, rec)

	t.Setenv("SP_TEST_WORKER_REF_TOKEN", "from-the-environment")

	org := models.NewOrganization("env-ref-e2e", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	const reference = "Bearer ${env:SP_TEST_WORKER_REF_TOKEN}"

	check := seedRefCheck(t, ctx, dbSvc, org.UID, "env-ref-e2e", string(secretCheckType),
		models.JSONMap{"url": "https://x.test", "body": reference})

	jobs, _, err := runner.backend.ClaimJobs(ctx, runner.getWorker().UID, nil, 10, 10, time.Minute)
	r.NoError(err)
	r.Len(jobs, 1)
	r.NoError(runner.executeJob(ctx, runner.logger, jobs[0]))

	r.Equal("Bearer from-the-environment", rec.get("body"))
	assertStoredConfigHoldsTheReference(
		t, ctx, dbSvc, org.UID, check.UID, "body", reference, "from-the-environment")
}

// TestUnresolvableReferenceIsAnErrorResultNotALiteral is the negative the spec
// insists on: a deleted parameter must show up in the check's history as an
// error, NOT as a probe that cheerfully posted the string "${param:…}" to the
// target — which, against an endpoint that does not enforce the credential,
// would have looked green.
//
//nolint:paralleltest // Test uses shared database state
func TestUnresolvableReferenceIsAnErrorResultNotALiteral(t *testing.T) {
	r := require.New(t)
	rec := &configRecorder{}
	runner, dbSvc, ctx := setupSecretRunner(t, nil, rec)

	org := models.NewOrganization("missing-ref-e2e", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := seedRefCheck(t, ctx, dbSvc, org.UID, "missing-ref-e2e", string(secretCheckType),
		models.JSONMap{"url": "https://x.test", "body": "password=${param:never-created}"})

	jobs, _, err := runner.backend.ClaimJobs(ctx, runner.getWorker().UID, nil, 10, 10, time.Minute)
	r.NoError(err)

	// The claim path resolves `param:` server-side, so an unresolvable one is
	// caught there and the job is dropped from the batch with an error result —
	// the same contract an unopenable credential envelope has. Whichever side
	// catches it, the two things that must be true are: an error result exists,
	// and the checker never ran.
	for _, job := range jobs {
		_ = runner.executeJob(ctx, runner.logger, job)
	}

	r.Nil(rec.get("body"), "the checker must never receive a config carrying an unresolved reference")

	results, err := dbSvc.GetLastResultForChecks(ctx, org.UID, []string{check.UID})
	r.NoError(err)

	got, ok := results[check.UID]
	r.True(ok, "an unresolvable reference must produce a result, not a silent skip")
	r.NotNil(got.Status)
	r.Equal(int(models.ResultStatusError), *got.Status)
	r.Contains(fmt.Sprint(got.Output), "unresolved secret reference: param:never-created")
}

// TestRealHTTPCheckSendsTheResolvedBody is the spec's own acceptance test,
// with the REAL http checker and a real server on the other end: the probe must
// post the resolved password while the check row keeps the reference.
//
// It deliberately does not stub the checker. A recording stub proves the
// worker's plumbing; only a real request proves the value survived FromMap,
// the request builder and the wire.
//
//nolint:paralleltest // Test uses shared database state
func TestRealHTTPCheckSendsTheResolvedBody(t *testing.T) {
	r := require.New(t)
	runner, dbSvc, ctx := setupSecretRunner(t, nil, &configRecorder{})

	// Undo the stub registry setupSecretRunner installs: this test wants the
	// production checker lookup.
	runner.getChecker = registry.GetChecker
	runner.parseConfig = registry.ParseConfig

	var (
		mu       sync.Mutex
		received string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)

		mu.Lock()
		received = string(body)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	org := models.NewOrganization("http-ref-e2e", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, "sso-password", "hunter2", true))

	const reference = "grant_type=password&username=probe&password=${param:sso-password}"

	check := seedRefCheck(t, ctx, dbSvc, org.UID, "http-ref-e2e", string(checkerdef.CheckTypeHTTP),
		models.JSONMap{"url": srv.URL, "method": http.MethodPost, "body": reference})

	jobs, _, err := runner.backend.ClaimJobs(ctx, runner.getWorker().UID, nil, 10, 10, time.Minute)
	r.NoError(err)
	r.Len(jobs, 1)
	r.NoError(runner.executeJob(ctx, runner.logger, jobs[0]))

	mu.Lock()
	got := received
	mu.Unlock()

	r.Equal("grant_type=password&username=probe&password=hunter2", got,
		"the target must receive the resolved password")

	assertStoredConfigHoldsTheReference(t, ctx, dbSvc, org.UID, check.UID, "body", reference, "hunter2")
}
