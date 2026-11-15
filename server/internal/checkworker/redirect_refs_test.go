package checkworker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// materializeConfig records the values the env/param references resolved to
// (spec 2026-11-15-01): on the in-process path from the overlay, on the agent
// path from StoredConfig, and for env from the resolver itself.
//
// t.Setenv forbids t.Parallel.
//
//nolint:paralleltest // t.Setenv forbids t.Parallel, subtests included.
func TestMaterializeConfigRecordsResolvedReferenceValues(t *testing.T) {
	t.Setenv("SP_TEST_REDIRECT_REF_ENV", "env-value")

	cfg := &config.Config{}
	cfg.Checkers.EnvRefAllowlist = []string{"SP_TEST_REDIRECT_REF_ENV"}
	worker := &CheckWorker{config: cfg}

	t.Run("in-process overlay", func(t *testing.T) {
		r := require.New(t)
		job := &models.CheckJob{
			Config: models.JSONMap{
				"headers": map[string]any{"X": "${param:k}", "E": "${env:SP_TEST_REDIRECT_REF_ENV}"},
				"url":     "https://a/?t=${param:k2}",
			},
			ParamOverlay: map[string]any{
				"headers": map[string]any{"X": "value-one", "E": "${env:SP_TEST_REDIRECT_REF_ENV}"},
				"url":     "https://a/?t=value-two",
			},
		}

		r.NoError(worker.materializeConfig(job))
		r.ElementsMatch([]string{"value-one", "value-two", "env-value"}, job.ResolvedRefValues)
	})

	t.Run("agent path, sealed overlay already merged", func(t *testing.T) {
		r := require.New(t)
		job := &models.CheckJob{
			StoredConfig: models.JSONMap{"headers": map[string]any{"X": "${param:k}"}},
			Config:       models.JSONMap{"headers": map[string]any{"X": "value-one"}},
		}

		r.NoError(worker.materializeConfig(job))
		r.Equal([]string{"value-one"}, job.ResolvedRefValues)
	})

	t.Run("no reference", func(t *testing.T) {
		r := require.New(t)
		job := &models.CheckJob{Config: models.JSONMap{"url": "https://a/", "headers": map[string]any{"X": "1"}}}

		r.NoError(worker.materializeConfig(job))
		r.Empty(job.ResolvedRefValues)
	})
}

// End to end, in process: a real HTTP check whose plain header holds a
// `${param:…}` reference does not send it to another origin it is redirected
// to, and the result names the header (spec 2026-11-15-01).
//
//nolint:paralleltest // Test uses shared database state
func TestRealHTTPCheckWithholdsReferencedHeaderOnCrossOriginRedirect(t *testing.T) {
	r := require.New(t)
	runner, dbSvc, ctx := setupSecretRunner(t, nil, &configRecorder{})
	runner.getChecker = registry.GetChecker
	runner.parseConfig = registry.ParseConfig

	var (
		mu       sync.Mutex
		received http.Header
	)

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		received = req.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, strings.Replace(other.URL, "127.0.0.1", "localhost", 1), http.StatusFound)
	}))
	defer origin.Close()

	org := models.NewOrganization("redirect-ref-e2e", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, paramkeys.StorageKey("api-key"), "hunter2-key", true))

	seedRefCheck(t, ctx, dbSvc, org.UID, "redirect-ref-e2e", string(checkerdef.CheckTypeHTTP),
		models.JSONMap{"url": origin.URL, "headers": map[string]any{"X-Api-Key": "${param:api-key}"}})

	jobs, _, err := runner.backend.ClaimJobs(ctx, runner.getWorker().UID, nil, 10, 10, time.Minute)
	r.NoError(err)
	r.Len(jobs, 1)
	r.NoError(runner.executeJob(ctx, runner.logger, jobs[0]))

	mu.Lock()
	got := received
	mu.Unlock()

	r.NotNil(got, "the redirect target was reached")
	r.Empty(got.Get("X-Api-Key"), "the referenced value must not reach another origin")

	result := lastResultForJob(t, dbSvc, ctx, jobs[0])
	r.Equal([]any{"X-Api-Key"}, result.Output["redirect_dropped_secret_headers"], result.Output)
}
