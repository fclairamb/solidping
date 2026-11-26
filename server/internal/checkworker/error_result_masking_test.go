package checkworker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// runParamURLJob runs an HTTP job whose stored URL is `<base>/${param:key}`
// with the parameter resolved to value, and returns the stored result. The
// reference sits in the path: url.Parse does not validate the escapes of a
// query, so a `%` followed by non-hex characters only breaks a path. An
// `http+unix` URL is parsed by the config (urlparse.Parse), so a value that
// breaks it fails there, before the probe.
func runParamURLJob(t *testing.T, base, value string, withRef bool) *models.Result {
	t.Helper()

	runner, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	check := models.NewCheck(org.UID, "mask-err-"+uuid.New().String()[:8], string(checkerdef.CheckTypeHTTP))
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	checkJob := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(checkJob).Where("check_uid = ?", check.UID).Scan(ctx))
	claimJobForTest(t, dbSvc, ctx, checkJob, worker.UID)

	checkJob.Type = string(checkerdef.CheckTypeHTTP)
	if withRef {
		checkJob.Config = models.JSONMap{"url": base + "/${param:key}"}
		checkJob.ParamOverlay = map[string]any{"url": base + "/" + value}
	} else {
		checkJob.Config = models.JSONMap{"url": base + "/" + value}
	}

	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))

	result := new(models.Result)
	require.NoError(t, dbSvc.DB().NewSelect().
		Model(result).
		Where("check_uid = ?", check.UID).
		Where("status != ?", int(models.ResultStatusCreated)).
		Scan(ctx))
	require.NotNil(t, result.Status)

	return result
}

func resultErrorText(t *testing.T, result *models.Result) string {
	t.Helper()

	text, ok := result.Output[checkerdef.OutputKeyError].(string)
	require.True(t, ok, "the result must carry an error text: %v", result.Output)

	return text
}

// TestErrorResultMasksResolvedReferenceValues is the regression of spec
// 2026-11-26-05: a resolved ${param:…} value that makes the URL fail to parse
// must not be stored in clear in the error result.
//
//nolint:paralleltest // Test uses shared database state
func TestErrorResultMasksResolvedReferenceValues(t *testing.T) {
	const (
		secret   = "acme%zzsecret"
		unixBase = "http+unix:///run/acme/app.sock:"
	)

	t.Run("config parse error masks the value", func(t *testing.T) {
		result := runParamURLJob(t, unixBase, secret, true)

		require.Equal(t, int(checkerdef.StatusError), *result.Status)
		text := resultErrorText(t, result)
		require.NotContains(t, text, secret)
		require.NotContains(t, text, "zzsecret")
		require.Contains(t, text, "***")
		require.Contains(t, text, ErrFailedToParseConf.Error(), "precondition: the pre-probe parse error branch")
		require.Contains(t, text, "invalid URL escape", "the error still tells what is wrong")
	})

	t.Run("value that parses gives a normal result", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		result := runParamURLJob(t, srv.URL, "acmegoodsecret", true)

		require.Equal(t, int(checkerdef.StatusUp), *result.Status, "output: %v", result.Output)
		blob, err := json.Marshal(result.Output)
		require.NoError(t, err)
		require.NotContains(t, string(blob), "acmegoodsecret")
	})

	t.Run("no resolved reference keeps the error text", func(t *testing.T) {
		result := runParamURLJob(t, unixBase, secret, false)

		require.Equal(t, int(checkerdef.StatusError), *result.Status)
		text := resultErrorText(t, result)
		require.Contains(t, text, ErrFailedToParseConf.Error())
		require.Contains(t, text, secret)
		require.NotContains(t, text, "***")
	})
}

func TestMaskErrorText(t *testing.T) {
	t.Parallel()

	t.Run("raw and Go-quoted spellings", func(t *testing.T) {
		t.Parallel()

		job := &models.CheckJob{ResolvedRefValues: []string{"zz\x01key", "zzplain"}}
		text := `parse "https://acme.com/zz\x01key": invalid control character; zzplain`

		got := maskErrorText(job, text)

		require.NotContains(t, got, "zz")
		require.Equal(t, 2, strings.Count(got, "***"))
	})

	t.Run("no resolved values leaves the text alone", func(t *testing.T) {
		t.Parallel()

		text := "parse error ***-free"
		require.Equal(t, text, maskErrorText(&models.CheckJob{}, text))
	})
}
