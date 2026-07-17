package checkworker

import (
	"context"
	"crypto/rand"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// This file covers spec 2026-07-16-05 end to end on the in-process worker:
// with encryption enabled, a check's secrets must actually reach the checker.
// Before the fix the worker read only checkJob.Config (the PUBLIC half), so an
// encrypted check probed its target with no credentials at all.

// e2eDEKStore is an in-memory credentials.DEKStore.
type e2eDEKStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *e2eDEKStore) LoadDEK(_ context.Context, orgUID string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.data[orgUID]

	return v, ok, nil
}

func (s *e2eDEKStore) SaveDEK(_ context.Context, orgUID string, wrapped []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[orgUID] = wrapped

	return nil
}

func newE2ECreds(t *testing.T) credentials.Service {
	t.Helper()

	key := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)

	svc, err := credentials.NewService(key, &e2eDEKStore{data: map[string][]byte{}})
	require.NoError(t, err)

	return svc
}

// configRecorder captures the config map the checker's FromMap received —
// i.e. exactly what the checker gets to authenticate with.
type configRecorder struct {
	mu   sync.Mutex
	seen map[string]any
}

func (r *configRecorder) record(m map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seen = make(map[string]any, len(m))
	for k, v := range m {
		r.seen[k] = v
	}
}

func (r *configRecorder) get(key string) any {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.seen[key]
}

type recordingConfig struct{ rec *configRecorder }

func (c recordingConfig) FromMap(m map[string]any) error {
	c.rec.record(m)

	return nil
}

func (c recordingConfig) GetConfig() map[string]any { return map[string]any{} }

const secretCheckType = checkerdef.CheckType("test-secret")

type recordingChecker struct{}

func (recordingChecker) Type() checkerdef.CheckType           { return secretCheckType }
func (recordingChecker) Validate(*checkerdef.CheckSpec) error { return nil }
func (recordingChecker) Execute(context.Context, checkerdef.Config) (*checkerdef.Result, error) {
	return &checkerdef.Result{Status: checkerdef.StatusUp}, nil
}

// setupSecretRunner builds a CheckWorker whose in-process DirectBackend is
// wired with the given credentials service — the exact production wiring
// (NewCheckWorker passes svc.Credentials through).
func setupSecretRunner(
	t *testing.T, creds credentials.Service, rec *configRecorder,
) (*CheckWorker, *sqlite.Service, context.Context) {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{
			CheckWorker: config.CheckWorkerConfig{Nb: 2, FetchMaxAhead: time.Minute},
		},
	}

	svcList := services.NewRegistry()
	checkJobSvc := checkjobsvc.NewService(dbSvc.DB())
	svcList.CheckJobs = checkJobSvc
	svcList.Credentials = creds

	eventNotifier := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = eventNotifier.Close() })
	svcList.EventNotifier = eventNotifier

	runner := NewCheckWorker(dbSvc, cfg, svcList, checkJobSvc)

	runner.getChecker = func(ct checkerdef.CheckType) (checkerdef.Checker, bool) {
		if ct == secretCheckType {
			return recordingChecker{}, true
		}

		return nil, false
	}
	runner.parseConfig = func(ct checkerdef.CheckType) (checkerdef.Config, bool) {
		if ct == secretCheckType {
			return recordingConfig{rec: rec}, true
		}

		return nil, false
	}

	worker := models.NewWorker("secret-worker", "Secret Worker")
	_, err = dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	return runner, dbSvc, ctx
}

// seedSecretCheck creates a check whose job carries the public config plus an
// encrypted envelope, mirroring what the scheduler materializes.
//
//nolint:revive // Test helper, context parameter order matches the package convention.
func seedSecretCheck(
	t *testing.T, ctx context.Context, dbSvc *sqlite.Service, orgUID, slug, envelope string,
) *models.Check {
	t.Helper()

	check := models.NewCheck(orgUID, slug, string(secretCheckType))
	check.Config = models.JSONMap{"url": "https://x.test"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	due := time.Now().Add(-time.Second)

	_, err := dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("config = ?", models.JSONMap{"url": "https://x.test"}).
		Set("config_private = ?", envelope).
		Set("config_private_keys = ?", `["password"]`).
		Set("encrypted = ?", true).
		Set("scheduled_at = ?", due).
		Set("effective_scheduled_at = ?", due).
		Where("check_uid = ?", check.UID).
		Exec(ctx)
	require.NoError(t, err)

	return check
}

// TestInProcessWorkerCheckerReceivesEncryptedSecret is the end-to-end proof of
// the spec: claim through the worker's own backend, execute through the real
// executeJob path, and assert the checker's FromMap saw the decrypted
// password. This test fails without the fix — the checker used to receive the
// public half only.
//
//nolint:paralleltest // Test uses shared database state
func TestInProcessWorkerCheckerReceivesEncryptedSecret(t *testing.T) {
	r := require.New(t)
	rec := &configRecorder{}
	creds := newE2ECreds(t)
	runner, dbSvc, ctx := setupSecretRunner(t, creds, rec)

	org := models.NewOrganization("secret-e2e", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	envelope, err := creds.EncryptForOrg(ctx, org.UID, map[string]any{"password": "hunter2"})
	r.NoError(err)

	check := seedSecretCheck(t, ctx, dbSvc, org.UID, "secret-e2e", envelope)

	// The exact call the pool fetcher makes.
	jobs, err := runner.backend.ClaimJobs(ctx, runner.getWorker().UID, nil, 10, 10, time.Minute)
	r.NoError(err)
	r.Len(jobs, 1)

	r.NoError(runner.executeJob(ctx, runner.logger, jobs[0]))

	r.Equal("hunter2", rec.get("password"),
		"the checker must receive the decrypted secret from config_private")
	r.Equal("https://x.test", rec.get("url"))

	// The secret must not surface in what gets persisted and rendered.
	results, err := dbSvc.GetLastResultForChecks(ctx, org.UID, []string{check.UID})
	r.NoError(err)
	got, ok := results[check.UID]
	r.True(ok)
	r.NotContains(mapString(got.Output), "hunter2")
	r.NotContains(mapString(got.Metrics), "hunter2")
}

// TestInProcessWorkerPlaintextConfigUnchanged pins the encryption-disabled
// fallback end to end: with no master key and no envelope, the plaintext
// secret in the public config still reaches the checker.
//
//nolint:paralleltest // Test uses shared database state
func TestInProcessWorkerPlaintextConfigUnchanged(t *testing.T) {
	r := require.New(t)
	rec := &configRecorder{}
	runner, dbSvc, ctx := setupSecretRunner(t, nil, rec)

	org := models.NewOrganization("plain-e2e", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "plain-e2e", string(secretCheckType))
	check.Config = models.JSONMap{"url": "https://x.test", "password": "plaintext"}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	due := time.Now().Add(-time.Second)
	_, err := dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("config = ?", check.Config).
		Set("scheduled_at = ?", due).
		Set("effective_scheduled_at = ?", due).
		Where("check_uid = ?", check.UID).
		Exec(ctx)
	r.NoError(err)

	jobs, err := runner.backend.ClaimJobs(ctx, runner.getWorker().UID, nil, 10, 10, time.Minute)
	r.NoError(err)
	r.Len(jobs, 1)

	r.NoError(runner.executeJob(ctx, runner.logger, jobs[0]))

	r.Equal("plaintext", rec.get("password"),
		"the documented plaintext fallback must keep working when no master key is set")
}

// mapString renders a JSONMap for leak assertions.
func mapString(m models.JSONMap) string {
	var out strings.Builder

	for k, v := range m {
		out.WriteString(k)
		out.WriteString("=")

		if s, ok := v.(string); ok {
			out.WriteString(s)
		}

		out.WriteString(";")
	}

	return out.String()
}
