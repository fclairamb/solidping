package checkworker

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	checkbrowserconfig "github.com/fclairamb/solidping/server/internal/checkers/checkbrowser/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestResolveExtraBudget pins the spec 2026-09-25-35 seam: a browser check
// with capture opted in (or a run carrying a forced/on-demand capture, spec
// 2026-09-25-34) grows the HARD execution deadline by its config's own
// ExtraBudget, while a check with no capture in play, or a config type that
// declares none at all, is unchanged (positive/negative control).
func TestResolveExtraBudget(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	tests := []struct {
		name          string
		config        checkerdef.Config
		forcedCapture bool
		want          time.Duration
	}{
		{
			name:   "browser check with screenshot opted in",
			config: &checkbrowserconfig.BrowserConfig{URL: "https://example.com", Screenshot: true},
			want:   checkbrowserconfig.ScreenshotTimeout,
		},
		{
			name:   "browser check with screenshot NOT opted in",
			config: &checkbrowserconfig.BrowserConfig{URL: "https://example.com"},
			want:   0,
		},
		{
			name:          "browser check forced by an on-demand capture, screenshot not opted in",
			config:        &checkbrowserconfig.BrowserConfig{URL: "https://example.com"},
			forcedCapture: true,
			want:          checkbrowserconfig.ScreenshotTimeout,
		},
		{
			name:   "non-browser config (http) declares no ExtraBudgeter at all",
			config: plainConfig{},
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r.Equal(tt.want, resolveExtraBudget(tt.config, tt.forcedCapture))
		})
	}
}

// extraBudgetRecordingConfig is a minimal checkerdef.Config that also
// implements checkerdef.ExtraBudgeter, mirroring
// checkbrowserconfig.BrowserConfig.ExtraBudget's exact shape (opted in via a
// boolean the config carries, or forced regardless of it) without depending
// on the real checkbrowser package. It records the `timeout` value FromMap
// received — the same technique timeoutRecordingConfig uses in
// worker_test.go — so a test can assert executeJob still threads the
// checker's OWN checkTimeout, never the extended value, into the config
// (spec 2026-09-25-35: "only the ceiling moves").
type extraBudgetRecordingConfig struct {
	rec        *timeoutRecorder
	screenshot bool
}

func (c *extraBudgetRecordingConfig) FromMap(configMap map[string]any) error {
	if raw, ok := configMap[checkTimeoutConfigKey].(string); ok {
		c.rec.seen = true
		c.rec.value = raw
	}

	return nil
}

func (c *extraBudgetRecordingConfig) GetConfig() map[string]any { return map[string]any{} }

func (c *extraBudgetRecordingConfig) ExtraBudget(forcedCapture bool) time.Duration {
	if c.screenshot || forcedCapture {
		return checkbrowserconfig.ScreenshotTimeout
	}

	return 0
}

// extraBudgetCaseSetup is what each TestExecuteJob_ExtraBudgetExtendsTheReal
// -ExecutionDeadline case needs registered under a fresh synthetic check
// type: the config resolveExtraBudget will type-assert against.
type extraBudgetCaseSetup struct {
	checkType checkerdef.CheckType
	config    checkerdef.Config
}

// TestExecuteJob_ExtraBudgetExtendsTheRealExecutionDeadline drives the REAL
// executeJob path (spec 2026-09-25-35) end to end — not just
// resolveExtraBudget in isolation — and asserts the ACTUAL context deadline
// handed to checker.Execute via deadlineRecordingChecker (worker_test.go),
// exactly the technique TestExecuteJob_ExecutionContextIsCheckTimeoutPlusOneSecond
// already uses to pin the pre-existing +1s margin.
//
// getChecker is always stubbed to the recorder (so the deadline is
// observable without a real network call); parseConfig varies per case,
// including one case that resolves to the REAL registry-produced
// checkhttp config type, proving resolveExtraBudget's type assertion
// against production code — not just a hand-written fake — returns nothing
// to add.
//
//nolint:paralleltest // Test uses shared database state
func TestExecuteJob_ExtraBudgetExtendsTheRealExecutionDeadline(t *testing.T) {
	const checkTimeout = 3 * time.Second

	tests := []struct {
		name             string
		build            func(t *testing.T, rec *timeoutRecorder) extraBudgetCaseSetup
		captureRequested bool
		wantExtra        time.Duration
	}{
		{
			name: "browser-shaped config, screenshot opted in",
			build: func(_ *testing.T, rec *timeoutRecorder) extraBudgetCaseSetup {
				return extraBudgetCaseSetup{
					checkType: checkerdef.CheckType("test-extra-budget-screenshot-on"),
					config:    &extraBudgetRecordingConfig{rec: rec, screenshot: true},
				}
			},
			wantExtra: checkbrowserconfig.ScreenshotTimeout,
		},
		{
			name: "browser-shaped config, screenshot off but the job carries a forced/on-demand capture",
			build: func(_ *testing.T, rec *timeoutRecorder) extraBudgetCaseSetup {
				return extraBudgetCaseSetup{
					checkType: checkerdef.CheckType("test-extra-budget-forced"),
					config:    &extraBudgetRecordingConfig{rec: rec, screenshot: false},
				}
			},
			captureRequested: true,
			wantExtra:        checkbrowserconfig.ScreenshotTimeout,
		},
		{
			name: "positive control: browser-shaped config, screenshot off, no on-demand request",
			build: func(_ *testing.T, rec *timeoutRecorder) extraBudgetCaseSetup {
				return extraBudgetCaseSetup{
					checkType: checkerdef.CheckType("test-extra-budget-off"),
					config:    &extraBudgetRecordingConfig{rec: rec, screenshot: false},
				}
			},
			wantExtra: 0,
		},
		{
			name: "positive control: the REAL http check config, which declares no ExtraBudgeter at all",
			build: func(t *testing.T, _ *timeoutRecorder) extraBudgetCaseSetup {
				t.Helper()

				httpConfig, ok := registry.ParseConfig(checkerdef.CheckTypeHTTP)
				require.True(t, ok, "http checker must be registered")

				return extraBudgetCaseSetup{
					checkType: checkerdef.CheckType("test-extra-budget-http"),
					config:    httpConfig,
				}
			},
			wantExtra: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, dbSvc, ctx := setupTestRunner(t)
			defer func() { _ = dbSvc.Close() }()

			org := models.NewOrganization("test-org", "")
			require.NoError(t, dbSvc.CreateOrganization(ctx, org))

			worker := models.NewWorker("test-worker", "Test Worker")
			_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
			require.NoError(t, err)
			runner.setWorker(worker)

			// Flat checkTimeout (cost-aware off, CostTimeoutFactor 0), exactly
			// like TestExecuteJob_ExecutionContextIsCheckTimeoutPlusOneSecond.
			runner.schedParams = scheduling.Params{CheckTimeout: checkTimeout}

			rec := &timeoutRecorder{}
			built := tt.build(t, rec)

			recorder := &deadlineRecordingChecker{}
			runner.getChecker = func(ct checkerdef.CheckType) (checkerdef.Checker, bool) {
				if ct == built.checkType {
					return recorder, true
				}

				return registry.GetChecker(ct)
			}
			runner.parseConfig = func(ct checkerdef.CheckType) (checkerdef.Config, bool) {
				if ct == built.checkType {
					return built.config, true
				}

				return registry.ParseConfig(ct)
			}

			check := models.NewCheck(org.UID, "extra-budget-"+uuid.New().String()[:8], string(built.checkType))
			require.NoError(t, dbSvc.CreateCheck(ctx, check))

			checkJob := new(models.CheckJob)
			require.NoError(t, dbSvc.DB().NewSelect().Model(checkJob).Where("check_uid = ?", check.UID).Scan(ctx))
			claimJobForTest(t, dbSvc, ctx, checkJob, worker.UID)

			if tt.captureRequested {
				// Mirrors the real claim: the DB column is consumed, but the
				// claimed struct in memory keeps the value (models.CheckJob's
				// own doc comment on CaptureRequestedAt) — which is exactly
				// what tells executeJob to force the capture and its budget.
				now := time.Now()
				checkJob.CaptureRequestedAt = &now
			}

			start := time.Now()
			require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))

			require.True(t, recorder.observed.Load(), "the checker must observe a context deadline")
			deadline := time.Unix(0, recorder.deadlineUnixNano.Load())
			observed := deadline.Sub(start)

			wantObserved := checkTimeout + time.Second + tt.wantExtra
			assert.InDelta(t, wantObserved.Seconds(), observed.Seconds(), 0.5,
				"execution context deadline must be checkTimeout + 1s + ExtraBudget")

			// The budget threaded into the checker's OWN config must stay
			// checkTimeout — never the extended value — for the cases that can
			// observe it (the real HTTPConfig case doesn't record it, but its
			// deadline assertion above already proves it got no extra budget
			// at all).
			if rec.seen {
				assert.Equal(t, checkTimeout.String(), rec.value,
					"only the hard deadline may grow; the checker's own budget must not")
			}
		})
	}
}
