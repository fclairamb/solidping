package checkworker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// runRefCheck seeds a check of the given type and URL in a fresh org whose
// `key` parameter holds paramValue, runs it in process and returns its result.
func runRefCheck(t *testing.T, checkType checkerdef.CheckType, slug, rawURL, paramValue string) *models.Result {
	t.Helper()

	r := require.New(t)
	runner, dbSvc, ctx := setupSecretRunner(t, nil, &configRecorder{})
	runner.getChecker = registry.GetChecker
	runner.parseConfig = registry.ParseConfig

	org := models.NewOrganization(slug, "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, paramkeys.StorageKey("key"), paramValue, true))

	seedRefCheck(t, ctx, dbSvc, org.UID, slug, string(checkType), models.JSONMap{"url": rawURL})

	jobs, _, err := runner.backend.ClaimJobs(ctx, runner.getWorker().UID, nil, 10, 10, time.Minute)
	r.NoError(err)
	r.Len(jobs, 1)
	r.NoError(runner.executeJob(ctx, runner.logger, jobs[0]))

	return lastResultForJob(t, dbSvc, ctx, jobs[0])
}

func storedErrorText(t *testing.T, result *models.Result) string {
	t.Helper()

	r := require.New(t)
	r.NotNil(result.Status)
	r.Equal(int(checkerdef.StatusError), *result.Status, result.Output)

	text, ok := result.Output[checkerdef.OutputKeyError].(string)
	r.True(ok, result.Output)

	return text
}

// A `${param:…}` value that only breaks the URL once substituted makes the
// config parse fail after the references are resolved (urlparse.Parse wraps the
// url.Parse error, which quotes the resolved URL): the stored error result must
// be masked like any other output (security scan 2026-11-26, AY1).
//
//nolint:paralleltest // Test uses shared database state
func TestConfigParseErrorResultMasksResolvedReferenceValues(t *testing.T) {
	t.Run("invalid escape fails the config parse", func(t *testing.T) {
		r := require.New(t)
		result := runRefCheck(t, checkerdef.CheckTypeTCP, "err-mask-escape",
			"tcp://acme.com:443/${param:key}", "%zzhunter2secret")

		text := storedErrorText(t, result)
		r.Contains(text, ErrFailedToParseConf.Error(), "precondition: the config-parse error branch")
		r.NotContains(text, "hunter2secret")
		r.Contains(text, resolvedValueMask)

		blob, err := json.Marshal(result.Output)
		r.NoError(err)
		r.NotContains(string(blob), "hunter2secret")
	})

	// The HTTP checker builds its request from the resolved URL and url.Error
	// quotes it with %q: the control character reaches the output escaped.
	t.Run("control character quoted by the url error", func(t *testing.T) {
		r := require.New(t)
		result := runRefCheck(t, checkerdef.CheckTypeHTTP, "err-mask-ctl",
			"https://acme.com/?k=${param:key}", "hunter2\nsecret")

		text := storedErrorText(t, result)
		r.NotContains(text, "hunter2")
		r.NotContains(text, "secret")
		r.Contains(text, resolvedValueMask)
	})
}

// Positive controls: a value that parses runs the check normally, and an error
// result for a job without any resolved reference keeps its text unchanged.
//
//nolint:paralleltest // Test uses shared database state
func TestConfigParseErrorResultMaskingControls(t *testing.T) {
	t.Run("a value that parses produces a normal result", func(t *testing.T) {
		r := require.New(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		result := runRefCheck(t, checkerdef.CheckTypeHTTP, "err-mask-ok", server.URL+"/?k=${param:key}", "hunter2secret")

		r.NotNil(result.Status)
		r.Equal(int(checkerdef.StatusUp), *result.Status, result.Output)
	})

	t.Run("an error without resolved reference keeps its text", func(t *testing.T) {
		r := require.New(t)
		result := runRefCheck(t, checkerdef.CheckTypeTCP, "err-mask-noref", "tcp://acme.com:443/%zzliteral", "unusedvalue")

		text := storedErrorText(t, result)
		r.Contains(text, ErrFailedToParseConf.Error(), "precondition: the config-parse error branch")
		r.Contains(text, "%zzliteral")
		r.NotContains(text, resolvedValueMask)
	})
}
