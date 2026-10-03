package checkhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhealth/config"
	httpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkhttp/config"
)

func serve(t *testing.T, status int, contentType, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server
}

func fixture(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("formats", "testdata", name+".json"))
	require.NoError(t, err)

	return string(raw)
}

func run(t *testing.T, cfg *HealthConfig) *checkerdef.Result {
	t.Helper()

	result, err := (&HealthChecker{}).Execute(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

func healthCfg(url string) *HealthConfig {
	return &HealthConfig{HTTPConfig: httpconfig.HTTPConfig{URL: url}}
}

func outputError(result *checkerdef.Result) string {
	text, _ := result.Output[checkerdef.OutputKeyError].(string)

	return text
}

func TestType(t *testing.T) {
	t.Parallel()
	require.Equal(t, checkerdef.CheckTypeHealth, (&HealthChecker{}).Type())
}

func TestAllOKIsUp(t *testing.T) {
	t.Parallel()

	body := `{"status":"UP","components":{"db":{"status":"UP"},"cache":{"status":"UP"}}}`
	result := run(t, healthCfg(serve(t, 200, "application/json", body).URL))

	require.Equal(t, checkerdef.StatusUp, result.Status)
	require.Equal(t, "spring", result.Output["format"])
	require.Empty(t, outputError(result))
	require.InDelta(t, 2, result.Metrics["components_total"], 0)
	require.InDelta(t, 0, result.Metrics["components_failed"], 0)
}

func TestSpring503IsJudgedByTheBody(t *testing.T) {
	t.Parallel()

	result := run(t, healthCfg(serve(t, 503, "application/json", fixture(t, "spring")).URL))

	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Contains(t, outputError(result), "mail: ")
	require.Contains(t, outputError(result), "connection refused")
	require.Contains(t, outputError(result), "broker.secondary")
	require.Equal(t, []string{"broker.secondary", "mail"}, result.Output["failed"])
	require.Equal(t, 503, result.Output[checkerdef.OutputKeyStatusCode])
}

func TestHTMLBodyIsNotRecognised(t *testing.T) {
	t.Parallel()

	result := run(t, healthCfg(serve(t, 503, "text/html", "<html>Service Unavailable</html>").URL))

	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Equal(t, "health response not recognised (HTTP 503)", outputError(result)) //nolint:misspell // spec wording
}

func TestJSONArrayIsNotRecognised(t *testing.T) {
	t.Parallel()

	result := run(t, healthCfg(serve(t, 200, "application/json", `[1,2]`).URL))

	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Contains(t, outputError(result), "health response")
}

func TestUnknownShapeIsNotRecognised(t *testing.T) {
	t.Parallel()

	result := run(t, healthCfg(serve(t, 200, "application/json", `{"hello":"world"}`).URL))

	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Contains(t, outputError(result), "health response")
}

func TestForcedFormatOnWrongBodyIsDown(t *testing.T) {
	t.Parallel()

	cfg := healthCfg(serve(t, 200, "application/json", fixture(t, "spatie")).URL)
	cfg.Format = "spring"

	result := run(t, cfg)

	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Contains(t, outputError(result), "health response")
}

// spatieBody builds a spatie document finished `age` ago.
func spatieBody(age time.Duration, status string) string {
	return fmt.Sprintf(`{"finishedAt":%d,"checkResults":[{"name":"Database","label":"Database",`+
		`"status":%q,"notificationMessage":"msg","shortSummary":"12 ms","meta":{}}]}`,
		time.Now().Add(-age).Unix(), status)
}

func TestStaleResults(t *testing.T) {
	t.Parallel()

	cfg := healthCfg(serve(t, 200, "application/json", spatieBody(11*time.Minute, "ok")).URL)
	cfg.MaxAge = "10m"

	result := run(t, cfg)

	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Equal(t, "health results are stale (last run 11 min ago)", outputError(result))
}

func TestDefaultMaxAgeIsTenMinutes(t *testing.T) {
	t.Parallel()

	result := run(t, healthCfg(serve(t, 200, "application/json", spatieBody(23*time.Minute, "ok")).URL))

	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Contains(t, outputError(result), "stale (last run 23 min ago)")
}

func TestMaxAgeZeroDisablesStaleness(t *testing.T) {
	t.Parallel()

	cfg := healthCfg(serve(t, 200, "application/json", spatieBody(3*time.Hour, "ok")).URL)
	cfg.MaxAge = "0"

	result := run(t, cfg)

	require.Equal(t, checkerdef.StatusUp, result.Status)
}

func TestFreshResultsAreNotStale(t *testing.T) {
	t.Parallel()

	cfg := healthCfg(serve(t, 200, "application/json", spatieBody(time.Minute, "ok")).URL)

	require.Equal(t, checkerdef.StatusUp, run(t, cfg).Status)
}

func TestFormatsWithoutTimestampSkipStaleness(t *testing.T) {
	t.Parallel()

	result := run(t, healthCfg(serve(t, 200, "application/json", fixture(t, "microprofile")).URL))

	require.NotContains(t, outputError(result), "stale")
}

func TestIgnoredFailedComponentIsUp(t *testing.T) {
	t.Parallel()

	cfg := healthCfg(serve(t, 200, "application/json", spatieBody(time.Minute, "failed")).URL)
	cfg.Ignore = []string{"Database"}

	result := run(t, cfg)

	require.Equal(t, checkerdef.StatusUp, result.Status)
	require.Empty(t, result.Output["failed"])
	require.InDelta(t, 0, result.Metrics["components_failed"], 0)

	components, ok := result.Output["components"].([]map[string]any)
	require.True(t, ok)
	require.Equal(t, true, components[0]["ignored"], "an ignored component is still shown")
}

func TestOnFailedWarningDowngrades(t *testing.T) {
	t.Parallel()

	cfg := healthCfg(serve(t, 200, "application/json", spatieBody(time.Minute, "failed")).URL)
	cfg.Components = map[string]ComponentOverride{"Database": {OnFailed: config.OnFailedWarning}}

	result := run(t, cfg)

	require.Equal(t, checkerdef.StatusWarning, result.Status)
	require.Empty(t, outputError(result))
}

func TestWarningComponentIsWarning(t *testing.T) {
	t.Parallel()

	result := run(t, healthCfg(serve(t, 200, "application/json", spatieBody(time.Minute, "warning")).URL))

	require.Equal(t, checkerdef.StatusWarning, result.Status)
	require.Equal(t, []string{"Database"}, result.Output["warning"])
	require.InDelta(t, 1, result.Metrics["components_warning"], 0)
}

func TestSkippedAndUnknownNeverChangeStatus(t *testing.T) {
	t.Parallel()

	body := `{"status":"UP","components":{"a":{"status":"UNKNOWN"},"b":{"status":"UP"}}}`
	require.Equal(t, checkerdef.StatusUp, run(t, healthCfg(serve(t, 200, "application/json", body).URL)).Status)

	result := run(t, healthCfg(serve(t, 200, "application/json", fixture(t, "spatie")).URL))
	// the fixture has failed and warning components; skipped Horizon is only shown
	components, ok := result.Output["components"].([]map[string]any)
	require.True(t, ok)

	var skipped bool

	for _, component := range components {
		if component["name"] == "Horizon" {
			skipped = component["status"] == "skipped"
		}
	}

	require.True(t, skipped)
}

func TestSimpleUsesOverall(t *testing.T) {
	t.Parallel()

	cases := map[string]checkerdef.Status{
		"ok":      checkerdef.StatusUp,
		"degrade": checkerdef.StatusDown, // unknown word
		"warn":    checkerdef.StatusWarning,
		"down":    checkerdef.StatusDown,
	}

	for word, want := range cases {
		result := run(t, healthCfg(serve(t, 200, "application/json", `{"status":"`+word+`"}`).URL))
		require.Equal(t, want, result.Status, word)
	}
}

func TestMoreThanHundredComponentsAreTruncated(t *testing.T) {
	t.Parallel()

	entries := make([]string, 0, 130)
	for i := range 130 {
		entries = append(entries, fmt.Sprintf(`"c%03d":{"status":"UP"}`, i))
	}

	body := `{"status":"UP","components":{` + strings.Join(entries, ",") + `}}`
	result := run(t, healthCfg(serve(t, 200, "application/json", body).URL))

	components, ok := result.Output["components"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, components, 100)
	require.Equal(t, true, result.Output["components_truncated"])
	require.InDelta(t, 130, result.Metrics["components_total"], 0)
}

func TestNotTruncatedUnderTheCap(t *testing.T) {
	t.Parallel()

	result := run(t, healthCfg(serve(t, 200, "application/json", fixture(t, "aspnet")).URL))

	require.NotContains(t, result.Output, "components_truncated")
}

func TestMetaMetricsCappedAtTwenty(t *testing.T) {
	t.Parallel()

	meta := make([]string, 0, 30)
	for i := range 30 {
		meta = append(meta, fmt.Sprintf(`"m%02d":%d`, i, i))
	}

	body := `{"status":"UP","components":{"db":{"status":"UP","details":{` + strings.Join(meta, ",") +
		`,"database":"PostgreSQL"}}}}`
	result := run(t, healthCfg(serve(t, 200, "application/json", body).URL))

	count := 0

	for key := range result.Metrics {
		if strings.HasPrefix(key, "meta.") {
			count++
		}
	}

	require.Equal(t, 20, count)
	require.Contains(t, result.Metrics, "meta.db.m00")
	require.Contains(t, result.Metrics, "meta.db.m19")
	require.NotContains(t, result.Metrics, "meta.db.m20", "first 20 in order")
	require.NotContains(t, result.Metrics, "meta.db.database", "non numeric stays in the output only")
}

func TestSecretHeaderIsSentAndNeverInTheOutput(t *testing.T) {
	t.Parallel()

	const secret = "s3cr3t-value-123"

	var got string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("oh-dear-health-check-secret")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(spatieBody(time.Minute, "failed")))
	}))
	t.Cleanup(server.Close)

	cfg := healthCfg(server.URL)
	cfg.SecretHeaders = map[string]string{"oh-dear-health-check-secret": secret}

	result := run(t, cfg)

	require.Equal(t, secret, got)
	require.Equal(t, checkerdef.StatusDown, result.Status)

	encoded, err := json.Marshal(result.Output)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), secret)
}

func TestNetworkFailureIsLikeHTTP(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	result := run(t, healthCfg(url))

	require.NotEqual(t, checkerdef.StatusUp, result.Status)
	require.NotNil(t, result.Diagnostics, "the http network failure diagnostics are kept")
	require.NotNil(t, result.Diagnostics.NetworkFailure)
}

func TestSamplesValidate(t *testing.T) {
	t.Parallel()

	samples := (&HealthChecker{}).GetSampleConfigs(nil)
	require.Len(t, samples, 4)

	formats := map[string]bool{}

	for _, sample := range samples {
		spec := sample
		require.NoError(t, (&HealthChecker{}).Validate(&spec), sample.Slug)

		formats[fmt.Sprint(sample.Config["format"])] = true
	}

	require.Equal(t, map[string]bool{"spatie": true, "spring": true, "aspnet": true, "ietf": true}, formats)
}

func TestValidateDelegatesToConfig(t *testing.T) {
	t.Parallel()

	err := (&HealthChecker{}).Validate(&checkerdef.CheckSpec{Config: map[string]any{
		"url": "https://app.acme.com/health", "body_expect": "ok",
	}})
	require.Error(t, err)
}
