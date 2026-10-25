package cli

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func one(path string, status int, body string) []coverRoute {
	return []coverRoute{{path: path, status: status, body: body}}
}

func exitCodeOf(err error) int {
	var coder cli.ExitCoder
	if err != nil && errors.As(err, &coder) {
		return coder.ExitCode()
	}

	return -1
}

func writeManifest(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte("checks: []\n"), 0o600))

	return path
}

const applyPlanBody = `{"manifest":"m","dryRun":true,"created":1,"updated":1,"unchanged":0,"deleted":0,"unmanaged":0,` +
	`"plan":[{"action":"create","slug":"web"},{"action":"update","slug":"api","previousSlug":"old-api"}],` +
	`"warnings":["careful"]}`

const applyErrBody = `{"manifest":"m","created":0,"errors":[{"slug":"web","error":"bad"}]}`

//nolint:paralleltest // coverRun swaps process-global stdio and HOME
func TestApplyActionBranches(t *testing.T) {
	manifest := writeManifest(t, "m.yaml")
	jsonManifest := writeManifest(t, "m.json")
	plan := one("apply", 200, applyPlanBody)

	tests := []struct {
		name     string
		routes   []coverRoute
		args     []string
		wantErr  bool
		wantCode int
	}{
		{"no file", plan, []string{"apply"}, true, 5},
		{"missing file", plan, []string{"apply", "-f", "/nonexistent/m.yaml"}, true, 5},
		{"dry run text", plan, []string{"apply", "-f", manifest, "--dry-run"}, false, 0},
		{"dry run positional json", plan, []string{"apply", "--dry-run", jsonManifest}, false, 0},
		{"dry run json output", plan, []string{"-o", "json", "apply", "-f", manifest, "--dry-run"}, false, 0},
		{"json without yes aborts", plan, []string{"-o", "json", "apply", "-f", manifest}, false, 0},
		{"json with yes", plan, []string{"-o", "json", "apply", "-f", manifest, "-y"}, false, 0},
		{"apply with yes", plan, []string{"apply", "-f", manifest, "-y", "--prune", "--force"}, false, 0},
		{"stdin declines", plan, []string{"apply", "-f", manifest}, true, 2},
		{"empty plan", one("apply", 200, `{"manifest":"m"}`), []string{"apply", "-f", manifest, "-y"}, false, 0},
		{"server errors reported", one("apply", 200, applyErrBody), []string{"apply", "-f", manifest, "-y"}, true, 1},
		{
			"plan request fails", one("apply", http.StatusForbidden, `{"code":"FORBIDDEN"}`),
			[]string{"apply", "-f", manifest, "-y"},
			true, -1,
		},
		{"plan body garbage", one("apply", 200, `nope`), []string{"apply", "-f", manifest, "-y"}, true, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := coverRun(t, tt.routes, tt.args...)
			if !tt.wantErr {
				require.NoError(t, err)

				return
			}
			require.Error(t, err)
			if tt.wantCode != -1 {
				require.Equal(t, tt.wantCode, exitCodeOf(err))
			}
		})
	}
}

//nolint:paralleltest // coverRun swaps process-global stdio and HOME
func TestChecksImportActionBranches(t *testing.T) {
	file := writeManifest(t, "export.yaml")
	ok := one("import", 200, `{"created":1,"updated":2,"skipped":0}`)
	bad := one("import", 200, `{"created":0,"errors":[{"index":1,"slug":"x","error":"bad"}]}`)

	require.Equal(t, 5, exitCodeOf(coverRun(t, ok, "checks", "import")))
	require.Equal(t, 5, exitCodeOf(coverRun(t, ok, "checks", "import", "/nonexistent/file.yaml")))
	require.NoError(t, coverRun(t, ok, "checks", "import", file))
	require.NoError(t, coverRun(t, ok, "checks", "import", "--dry-run", file))
	require.NoError(t, coverRun(t, ok, "-o", "json", "checks", "import", file))
	require.Equal(t, 1, exitCodeOf(coverRun(t, bad, "checks", "import", file)))
	require.Error(t, coverRun(t, one("import", 500, `{"code":"INTERNAL_ERROR"}`), "checks", "import", file))
	require.Error(t, coverRun(t, one("import", 200, `nope`), "checks", "import", file))
}

//nolint:paralleltest // coverRun swaps process-global stdio and HOME
func TestServerRenderersViaCommands(t *testing.T) {
	limits := `{"rateLimit":{"enabled":true,"requestsPerMinute":60,"burst":10,"callerRemaining":4.5},` +
		`"concurrency":{"enabled":true,"max":8,"callerInFlight":1},"callerIp":"1.2.3.4","callerBucket":"b"}`
	memory := `{"data":{"runtime":{"heapAllocBytes":1,"heapInuseBytes":2,"heapObjects":3,"stackInuseBytes":4,` +
		`"sysBytes":5,"numGoroutine":6,"numGc":7,"goMemLimitBytes":8,"goMaxProcs":9},"process":{"rssBytes":10},` +
		`"subsystems":{"dekCacheEntries":1,"rateLimitEntries":2,"eventListeners":3},` +
		`"build":{"cgoEnabled":true,"sqliteDriver":"modernc","goVersion":"go1.26"}}}`
	cost := `{"data":{"totalJobs":10,"thresholdMs":100,"fastJobs":7,"slowJobs":3,` +
		`"costEwmaMs":{"p50":1.5,"p90":2.5,"p99":3.5,"max":4.5},"delayEwmaMs":{"p50":1}}}`

	require.NoError(t, coverRun(t, one("limits", 200, limits), "server", "limits"))
	require.NoError(t, coverRun(t, one("memory", 200, memory), "server", "memory"))
	require.NoError(t, coverRun(t, one("cost", 200, cost), "server", "cost-distribution", "--threshold-ms", "50"))
	require.NoError(t, coverRun(t, one("cost", 200, `{}`), "server", "cost-distribution"))
	require.NoError(t, coverRun(t, one("cost", 200, cost), "-o", "json", "server", "cost-distribution"))
	require.Error(t, coverRun(t, one("limits", 500, `{}`), "server", "limits"))
	require.Error(t, coverRun(t, one("memory", 500, `{}`), "server", "memory"))
	require.Error(t, coverRun(t, one("cost", 500, `{}`), "server", "cost-distribution"))
	require.Error(t, coverRun(t, one("health", 500, `{}`), "server", "health"))
	require.Error(t, coverRun(t, one("version", 500, `{}`), "server", "version"))
}
