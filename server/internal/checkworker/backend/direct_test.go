package backend_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// getCheckCountingDB wraps a db.Service and counts GetCheck calls so the
// incident-processing tests can assert the claim-time check attachment removes
// the per-result GetCheck round-trip (behavior moved here from CheckWorker by
// the WorkerBackend refactor, spec 2026-07-16-02).
type getCheckCountingDB struct {
	db.Service
	getCheckCalls atomic.Int64
}

func (c *getCheckCountingDB) GetCheck(ctx context.Context, orgUID, checkUID string) (*models.Check, error) {
	c.getCheckCalls.Add(1)

	return c.Service.GetCheck(ctx, orgUID, checkUID)
}

func newDirectBackend(t *testing.T) (*backend.DirectBackend, *getCheckCountingDB, *sqlite.Service, context.Context) {
	t.Helper()

	be, counting, dbSvc, ctx := newDirectBackendWithCreds(t, nil)

	return be, counting, dbSvc, ctx
}

func newDirectBackendWithCreds(
	t *testing.T, creds credentials.Service,
) (*backend.DirectBackend, *getCheckCountingDB, *sqlite.Service, context.Context) {
	t.Helper()
	ctx := context.Background()

	sqliteSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, sqliteSvc.Initialize(ctx))
	t.Cleanup(func() { _ = sqliteSvc.Close() })

	counting := &getCheckCountingDB{Service: sqliteSvc}

	eventNotifier := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = eventNotifier.Close() })

	checkJobSvc := checkjobsvc.NewService(sqliteSvc.DB())
	incidentSvc := incidents.NewService(counting, nil, clock.Real{}, nil)

	return backend.NewDirectBackend(
		counting, checkJobSvc, incidentSvc, eventNotifier, creds,
	), counting, sqliteSvc, ctx
}

// submitReq builds a minimal successful-result submit request.
func submitReq() *backend.SubmitResultRequest {
	return &backend.SubmitResultRequest{
		Status:          int(models.ResultStatusUp),
		Duration:        1,
		NextScheduledAt: time.Now().Add(time.Minute),
	}
}

// registerWorker creates a workers row (results.worker_uid is a foreign key).
func registerWorker(ctx context.Context, t *testing.T, dbSvc db.Service, slug string) string {
	t.Helper()

	registered, err := dbSvc.RegisterOrUpdateWorker(ctx, models.NewWorker(slug, slug))
	require.NoError(t, err)

	return registered.UID
}

// TestSubmitResultUsesAttachedCheck pins the claim-time attachment contract:
// when the CheckJob carries an attached Check, incident processing must not
// call GetCheck.
func TestSubmitResultUsesAttachedCheck(t *testing.T) {
	t.Parallel()
	be, counting, dbSvc, ctx := newDirectBackend(t)

	org := models.NewOrganization("proc-inc-attached", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusUp
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	job := &models.CheckJob{
		UID:             "job-attached",
		OrganizationUID: org.UID,
		CheckUID:        check.UID,
		Check:           check, // attached at claim time
	}

	workerUID := registerWorker(ctx, t, dbSvc, "wk-attached")

	// The lease release may fail (no check_jobs row) — irrelevant here; the
	// assertion is about the GetCheck round-trip on the incident path.
	_ = be.SubmitResult(ctx, job, workerUID, submitReq())

	require.Equal(t, int64(0), counting.getCheckCalls.Load(),
		"incident processing must not call GetCheck when the job has an attached check")
}

// TestSubmitResultFallsBackToGetCheck pins the fallback path: when the attached
// check is nil, incident processing fetches it via GetCheck exactly once.
func TestSubmitResultFallsBackToGetCheck(t *testing.T) {
	t.Parallel()
	be, counting, dbSvc, ctx := newDirectBackend(t)

	org := models.NewOrganization("proc-inc-fallback", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusUp
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	job := &models.CheckJob{
		UID:             "job-fallback",
		OrganizationUID: org.UID,
		CheckUID:        check.UID,
		Check:           nil, // not attached -> fallback fetch
	}

	workerUID := registerWorker(ctx, t, dbSvc, "wk-fallback")

	_ = be.SubmitResult(ctx, job, workerUID, submitReq())

	require.Equal(t, int64(1), counting.getCheckCalls.Load(),
		"incident processing must fall back to a single GetCheck when no check is attached")
}

// TestSubmitResultWritesResultRow verifies the result row lands with the
// submitted fields (the DirectBackend keeps the pre-refactor write behavior).
func TestSubmitResultWritesResultRow(t *testing.T) {
	t.Parallel()
	be, _, dbSvc, ctx := newDirectBackend(t)

	org := models.NewOrganization("submit-writes", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	job := &models.CheckJob{
		UID:             "job-writes",
		OrganizationUID: org.UID,
		CheckUID:        check.UID,
		Check:           check,
	}

	region := "eu"
	req := submitReq()
	req.Region = &region
	req.Output = map[string]any{"message": "ok"}

	workerUID := registerWorker(ctx, t, dbSvc, "wk-writes")

	_ = be.SubmitResult(ctx, job, workerUID, req)

	results, err := dbSvc.GetLastResultForChecks(ctx, org.UID, []string{check.UID})
	require.NoError(t, err)
	got, ok := results[check.UID]
	require.True(t, ok, "a result row must exist for the check")
	require.NotNil(t, got.Status)
	require.Equal(t, int(models.ResultStatusUp), *got.Status)
	require.NotNil(t, got.Region)
	require.Equal(t, "eu", *got.Region)
}

// memDEKStore is an in-memory credentials.DEKStore for the encryption tests.
type memDEKStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *memDEKStore) LoadDEK(_ context.Context, orgUID string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.data[orgUID]

	return v, ok, nil
}

func (s *memDEKStore) SaveDEK(_ context.Context, orgUID string, wrapped []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[orgUID] = wrapped

	return nil
}

func newTestCreds(t *testing.T) credentials.Service {
	t.Helper()

	key := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)

	svc, err := credentials.NewService(key, &memDEKStore{data: map[string][]byte{}})
	require.NoError(t, err)

	return svc
}

// newOrg creates and persists an organization.
//
//nolint:revive // Test helper, context parameter order matches the file's convention.
func newOrg(t *testing.T, ctx context.Context, dbSvc db.Service, slug string) *models.Organization {
	t.Helper()

	org := models.NewOrganization(slug, "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	return org
}

// seedJob creates a check and rewrites the auto-created check_jobs row into
// the exact shape the scheduler materializes: public config in `config` plus
// (when envelope != "") the secrets in the `config_private` envelope
// (postgres.go:1172-1175 / checks/service.go:1974-1977). The job is due, in
// the fast lane, and unleased.
//
//nolint:revive // Test helper, context parameter order matches the file's convention.
func seedJob(
	t *testing.T,
	ctx context.Context,
	dbSvc db.Service,
	org *models.Organization,
	slug string,
	public models.JSONMap,
	envelope string,
) *models.Check {
	t.Helper()

	check := models.NewCheck(org.UID, slug, "http")
	check.Config = public
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	due := time.Now().Add(-time.Second)

	update := dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("config = ?", public).
		Set("scheduled_at = ?", due).
		Set("effective_scheduled_at = ?", due).
		Set("lane = ?", scheduling.LaneFast).
		Where("check_uid = ?", check.UID)

	if envelope != "" {
		update = update.
			Set("config_private = ?", envelope).
			Set("config_private_keys = ?", `["password"]`).
			Set("encrypted = ?", true)
	}

	_, err := update.Exec(ctx)
	require.NoError(t, err)

	return check
}

// publicOnly is the public half of an encrypted check's config: the secret
// key is absent — that is exactly what the bug shipped to the checker.
func publicOnly() models.JSONMap {
	return models.JSONMap{"url": "https://x.test"}
}

// claimOne claims through the backend and returns the single job for the
// given check, or nil when the job was dropped from the batch.
func claimOne(ctx context.Context, t *testing.T, be *backend.DirectBackend, workerUID, checkUID string,
) *models.CheckJob {
	t.Helper()

	jobs, err := be.ClaimJobs(ctx, workerUID, nil, 10, 10, time.Minute)
	require.NoError(t, err)

	for _, job := range jobs {
		if job.CheckUID == checkUID {
			return job
		}
	}

	return nil
}

// TestClaimJobsMergesEncryptedSecrets is THE regression test for spec
// 2026-07-16-05: before the fix, the in-process claim path handed the worker
// the PUBLIC config only, so an encrypted check ran with none of its
// credentials — silently. The claimed job must come back merged, with the
// envelope stripped.
func TestClaimJobsMergesEncryptedSecrets(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	creds := newTestCreds(t)
	be, _, dbSvc, ctx := newDirectBackendWithCreds(t, creds)

	org := newOrg(t, ctx, dbSvc, "claim-merge")

	envelope, err := creds.EncryptForOrg(ctx, org.UID, map[string]any{"password": "s3cr3t"})
	r.NoError(err)

	check := seedJob(t, ctx, dbSvc, org, "claim-merge", publicOnly(), envelope)
	workerUID := registerWorker(ctx, t, dbSvc, "wk-merge")

	job := claimOne(ctx, t, be, workerUID, check.UID)
	r.NotNil(job, "the encrypted job must still be dispatched")
	r.Equal("s3cr3t", job.Config["password"],
		"the checker must receive the decrypted secret, not the public half alone")
	r.Equal("https://x.test", job.Config["url"])
	r.Nil(job.ConfigPrivate, "the envelope must never reach the worker loop")
	r.Nil(job.ConfigPrivateKeys)
	r.False(job.Encrypted)
}

// TestClaimJobsForCheckMergesEncryptedSecrets pins the same contract on the
// express path (a freshly created check bypasses the pool fetcher).
func TestClaimJobsForCheckMergesEncryptedSecrets(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	creds := newTestCreds(t)
	be, _, dbSvc, ctx := newDirectBackendWithCreds(t, creds)

	org := newOrg(t, ctx, dbSvc, "express-merge")

	envelope, err := creds.EncryptForOrg(ctx, org.UID, map[string]any{"password": "express-pass"})
	r.NoError(err)

	check := seedJob(t, ctx, dbSvc, org, "express-merge", publicOnly(), envelope)
	workerUID := registerWorker(ctx, t, dbSvc, "wk-express")

	jobs, err := be.ClaimJobsForCheck(ctx, workerUID, nil, check.UID)
	r.NoError(err)
	r.Len(jobs, 1)
	r.Equal("express-pass", jobs[0].Config["password"])
	r.Nil(jobs[0].ConfigPrivate)
}

// TestClaimJobsPassesThroughPlaintextConfig pins the encryption-disabled
// fallback: with no master key and no envelope, the public config (secrets
// included) reaches the worker untouched.
func TestClaimJobsPassesThroughPlaintextConfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	be, _, dbSvc, ctx := newDirectBackendWithCreds(t, nil)

	org := newOrg(t, ctx, dbSvc, "claim-plain")
	check := seedJob(t, ctx, dbSvc, org, "claim-plain",
		models.JSONMap{"url": "https://x.test", "password": "plain-pass"}, "")
	workerUID := registerWorker(ctx, t, dbSvc, "wk-plain")

	job := claimOne(ctx, t, be, workerUID, check.UID)
	r.NotNil(job, "a plaintext job must never be dropped")
	r.Equal("plain-pass", job.Config["password"])
	r.Equal("https://x.test", job.Config["url"])
}

// TestClaimJobsDropsUndecryptableJobWithErrorResult pins the failure
// semantics chosen for the in-process path (spec item 3): a job whose
// envelope cannot be opened is NOT dispatched (running it without its
// secrets is the bug) and NOT silently skipped — it produces an explicit
// error result carrying the actionable reason, which also releases the lease.
func TestClaimJobsDropsUndecryptableJobWithErrorResult(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	// Encrypt with one key, claim with a backend holding a different one.
	foreign := newTestCreds(t)
	be, _, dbSvc, ctx := newDirectBackendWithCreds(t, newTestCreds(t))

	org := newOrg(t, ctx, dbSvc, "claim-baddec")

	envelope, err := foreign.EncryptForOrg(ctx, org.UID, map[string]any{"password": "s3cr3t"})
	r.NoError(err)

	check := seedJob(t, ctx, dbSvc, org, "claim-baddec", publicOnly(), envelope)
	workerUID := registerWorker(ctx, t, dbSvc, "wk-baddec")

	r.Nil(claimOne(ctx, t, be, workerUID, check.UID),
		"a job whose secrets cannot be decrypted must never be dispatched")

	results, err := dbSvc.GetLastResultForChecks(ctx, org.UID, []string{check.UID})
	r.NoError(err)
	got, ok := results[check.UID]
	r.True(ok, "the failure must be visible as a result, not a silent skip")
	r.NotNil(got.Status)
	r.Equal(int(models.ResultStatusError), *got.Status)
	r.Equal(checkjobsvc.ErrSecretsUndecryptable.Error(), got.Output["error"])
	// The secret must not leak into what the dashboard renders.
	r.NotContains(fmt.Sprint(got.Output), "s3cr3t")
	r.NotContains(fmt.Sprint(got.Metrics), "s3cr3t")

	// The lease was released, so the job is reschedulable rather than wedged.
	var job models.CheckJob
	r.NoError(dbSvc.DB().NewSelect().Model(&job).Where("check_uid = ?", check.UID).Scan(ctx))
	r.Nil(job.LeaseWorkerUID)
}

// TestClaimJobsDropsEncryptedJobWithoutMasterKey covers the other failure
// mode: an envelope exists but this process has no master key at all.
func TestClaimJobsDropsEncryptedJobWithoutMasterKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	be, _, dbSvc, ctx := newDirectBackendWithCreds(t, nil)

	org := newOrg(t, ctx, dbSvc, "claim-nokey")
	envelope := `{"v":1,"alg":"AES-256-GCM","nonce":"","ct":""}`

	check := seedJob(t, ctx, dbSvc, org, "claim-nokey", publicOnly(), envelope)
	workerUID := registerWorker(ctx, t, dbSvc, "wk-nokey")

	r.Nil(claimOne(ctx, t, be, workerUID, check.UID))

	results, err := dbSvc.GetLastResultForChecks(ctx, org.UID, []string{check.UID})
	r.NoError(err)
	got, ok := results[check.UID]
	r.True(ok)
	r.Equal(int(models.ResultStatusError), *got.Status)
	r.Equal(checkjobsvc.ErrSecretsUnavailable.Error(), got.Output["error"])
}

// logCapture is a concurrency-safe io.Writer backing a slog handler, so a test
// can assert on what the code under test actually logged.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.buf.Write(p)
}

func (c *logCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.buf.String()
}

// captureDefaultLogs swaps slog's default logger for one writing everything
// (down to Debug) into the returned capture, restoring the original on
// cleanup. DirectBackend logs through the package-level slog functions, so
// this is the only seam that sees what it emits.
//
// Callers must NOT be parallel: slog's default logger is process-global. Go
// runs non-parallel tests to completion before resuming parallel ones, so a
// sequential test gets the swap to itself.
func captureDefaultLogs(t *testing.T) *logCapture {
	t.Helper()

	capture := &logCapture{}
	previous := slog.Default()

	slog.SetDefault(slog.New(slog.NewTextHandler(capture, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return capture
}

// TestClaimJobsNeverLogsDecryptedSecrets pins the spec's no-leak requirement on
// the logging side. The decrypt-FAILURE branch is the one that logs (it hands
// slog the wrapped decrypt error), so it is where a careless error wrap would
// leak the plaintext; the success branch is asserted alongside it so neither
// path can start logging config.
//
// The assertion is deliberately non-vacuous: it first proves the capture is
// live (the known failure message IS present), so "secret absent" cannot pass
// merely because nothing was captured at all.
//
//nolint:paralleltest // Swaps the process-global slog default logger.
func TestClaimJobsNeverLogsDecryptedSecrets(t *testing.T) {
	r := require.New(t)
	capture := captureDefaultLogs(t)

	const secret = "logs-must-never-see-me"

	foreign := newTestCreds(t)
	creds := newTestCreds(t)
	be, _, dbSvc, ctx := newDirectBackendWithCreds(t, creds)

	// 1. A job this backend CAN decrypt: the success path must stay silent
	//    about the plaintext it just merged.
	okOrg := newOrg(t, ctx, dbSvc, "log-ok")
	okEnvelope, err := creds.EncryptForOrg(ctx, okOrg.UID, map[string]any{"password": secret})
	r.NoError(err)
	okCheck := seedJob(t, ctx, dbSvc, okOrg, "log-ok", publicOnly(), okEnvelope)

	// 2. A job it CANNOT decrypt (encrypted under a foreign key): the failure
	//    path logs, and must log the reason without the ciphertext's contents.
	badOrg := newOrg(t, ctx, dbSvc, "log-bad")
	badEnvelope, err := foreign.EncryptForOrg(ctx, badOrg.UID, map[string]any{"password": secret})
	r.NoError(err)
	badCheck := seedJob(t, ctx, dbSvc, badOrg, "log-bad", publicOnly(), badEnvelope)

	workerUID := registerWorker(ctx, t, dbSvc, "wk-logs")

	jobs, err := be.ClaimJobs(ctx, workerUID, nil, 10, 10, time.Minute)
	r.NoError(err)

	var okJob *models.CheckJob

	for _, job := range jobs {
		r.NotEqual(badCheck.UID, job.CheckUID, "the undecryptable job must be dropped")

		if job.CheckUID == okCheck.UID {
			okJob = job
		}
	}

	r.NotNil(okJob)
	r.Equal(secret, okJob.Config["password"], "the decryptable job must still be merged")

	logs := capture.String()
	// Non-vacuous: the capture is demonstrably live and holds the failure log.
	r.Contains(logs, "Cannot decrypt claimed job credentials",
		"the capture must be wired to what DirectBackend logs")
	r.NotContains(logs, secret,
		"a decrypted secret must never reach the logs, on either the success or the failure path")
	r.NotContains(logs, badEnvelope, "the raw envelope must not be logged either")
	r.NotContains(logs, okEnvelope)
}

// newPostgresDirectBackend builds a DirectBackend over a real embedded
// PostgreSQL, so the decrypt-failure policy can be asserted on the driver that
// actually runs in production. Skips (rather than fails) when embedded
// Postgres is unavailable, mirroring checkjobsvc/service_postgres_test.go.
// Uses port 15439 — distinct from 15434/15436/15437/15438.
func newPostgresDirectBackend(
	t *testing.T, creds credentials.Service,
) (*backend.DirectBackend, *postgres.Service, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     15439,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	eventNotifier := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = eventNotifier.Close() })

	checkJobSvc := checkjobsvc.NewService(dbSvc.DB())
	incidentSvc := incidents.NewService(dbSvc, nil, clock.Real{}, nil)

	return backend.NewDirectBackend(
		dbSvc, checkJobSvc, incidentSvc, eventNotifier, creds,
	), dbSvc, ctx
}

// TestClaimJobsDropsUndecryptableJobOnPostgres is the PostgreSQL half of the
// decrypt-failure semantics (spec item 4; the SQLite half is
// TestClaimJobsDropsUndecryptableJobWithErrorResult).
//
// The branch CONDITION is dialect-agnostic Go, but its EFFECT is not: dropping
// the job means SubmitResult → SaveResultWithStatusTracking + ReleaseLease,
// which is driver code. This pins that the error result really lands and the
// lease really releases on Postgres, not just on SQLite.
//
// Self-skips under `-short` like its embedded-Postgres siblings.
func TestClaimJobsDropsUndecryptableJobOnPostgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	r := require.New(t)

	// Encrypt under one key, claim with a backend holding a different one.
	foreign := newTestCreds(t)
	be, dbSvc, ctx := newPostgresDirectBackend(t, newTestCreds(t))

	org := newOrg(t, ctx, dbSvc, "pgbaddec")

	envelope, err := foreign.EncryptForOrg(ctx, org.UID, map[string]any{"password": "pg-s3cr3t"})
	r.NoError(err)

	check := seedJob(t, ctx, dbSvc, org, "pgbaddec", publicOnly(), envelope)
	workerUID := registerWorker(ctx, t, dbSvc, "wk-pgbaddec")

	jobs, err := be.ClaimJobs(ctx, workerUID, nil, 10, 10, time.Minute)
	r.NoError(err)

	for _, job := range jobs {
		r.NotEqual(check.UID, job.CheckUID,
			"a job whose secrets cannot be decrypted must never be dispatched on Postgres either")
	}

	// The failure is visible as a result row, not a silent skip.
	results, err := dbSvc.GetLastResultForChecks(ctx, org.UID, []string{check.UID})
	r.NoError(err)
	got, ok := results[check.UID]
	r.True(ok, "the error result must land on Postgres")
	r.NotNil(got.Status)
	r.Equal(int(models.ResultStatusError), *got.Status)
	r.Equal(checkjobsvc.ErrSecretsUndecryptable.Error(), got.Output["error"])
	r.NotContains(fmt.Sprint(got.Output), "pg-s3cr3t")

	// The lease was released, so the job is reschedulable rather than wedged.
	var job models.CheckJob
	r.NoError(dbSvc.DB().NewSelect().Model(&job).Where("check_uid = ?", check.UID).Scan(ctx))
	r.Nil(job.LeaseWorkerUID, "the lease must be released on Postgres too")
}
