package checktestrun

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

type neverRunner struct{ called bool }

func (n *neverRunner) Test(
	_ context.Context, _ string, _, _ map[string]string, _ time.Duration,
) (*aichecks.RunResult, error) {
	n.called = true

	return &aichecks.RunResult{}, nil
}

func TestTestRunTooManyInProgress(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	runner := &neverRunner{}
	handler := NewHandler(runner, &config.Config{})

	for range maxConcurrent {
		handler.slots <- struct{}{}
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orgs/acme/checks/test-run",
		strings.NewReader(`{"type":"js","config":{"script":"return { status: 'up' };"}}`))
	rec := httptest.NewRecorder()

	r.NoError(handler.TestRun(rec, req))
	r.Equal(http.StatusTooManyRequests, rec.Code)
	r.Equal("5", rec.Header().Get("Retry-After"))
	r.False(runner.called, "no script runs when every slot is busy")
}

var errRunnerBoom = errors.New("boom")

// scriptedRunner answers every run with a fixed outcome and records the call.
type scriptedRunner struct {
	result *aichecks.RunResult
	err    error

	script  string
	env     map[string]string
	secrets map[string]string
	timeout time.Duration
}

func (s *scriptedRunner) Test(
	_ context.Context, script string, env, secrets map[string]string, timeout time.Duration,
) (*aichecks.RunResult, error) {
	s.script, s.env, s.secrets, s.timeout = script, env, secrets, timeout

	return s.result, s.err
}

func postTestRun(t *testing.T, handler *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orgs/acme/checks/test-run",
		strings.NewReader(body))
	rec := httptest.NewRecorder()

	require.NoError(t, handler.TestRun(rec, req))

	return rec
}

func TestTestRunRejectsBadRequests(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		body    string
		message string
	}{
		"not json":       {`{"type":`, "Invalid JSON body"},
		"not a js check": {`{"type":"http","config":{"script":"x"}}`, "Only js checks can be test-run"},
		"blank script":   {`{"type":"js","config":{"script":"  \n"}}`, "script is required"},
		"bad timeout":    {`{"type":"js","config":{"script":"x","timeout":"soon"}}`, "Invalid timeout"},
		"negative":       {`{"type":"js","config":{"script":"x","timeout":"-5s"}}`, "Invalid timeout"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			runner := &neverRunner{}
			rec := postTestRun(t, NewHandler(runner, &config.Config{}), tc.body)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), tc.message)
			require.False(t, runner.called, "a rejected request never runs a script")
		})
	}
}

func TestTestRunReturnsTheRunOutcome(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	runner := &scriptedRunner{result: &aichecks.RunResult{
		Status:     "down",
		DurationMs: 42,
		Output: map[string]any{
			checkjs.OutputKeySteps: []any{map[string]any{"name": "Go to home", "status": "up"}},
			"error":                "step 2 failed",
		},
		Metrics: map[string]any{"steps": 2},
		Screenshot: &checkerdef.Screenshot{
			Image:  []byte("png-bytes"),
			Format: checkerdef.ImageFormatPNG,
		},
	}}

	rec := postTestRun(t, NewHandler(runner, &config.Config{}), `{"type":"js","config":{
	  "script":"return { status: 'up' };","env":{"A":"1"},"secrets":{"S":"x"},"timeout":"15s"}}`)

	r.Equal(http.StatusOK, rec.Code)
	r.Equal("return { status: 'up' };", runner.script)
	r.Equal(map[string]string{"A": "1"}, runner.env)
	r.Equal(map[string]string{"S": "x"}, runner.secrets)
	r.Equal(15*time.Second, runner.timeout)

	var got Response

	r.NoError(json.Unmarshal(rec.Body.Bytes(), &got))

	r.Equal("down", got.Status)
	r.EqualValues(42, got.DurationMs)
	r.Equal("step 2 failed", got.Message)
	r.Len(got.Steps, 1)
	r.NotNil(got.Screenshot)
	r.Equal("png", got.Screenshot.Format)
	r.Equal(base64.StdEncoding.EncodeToString([]byte("png-bytes")), got.Screenshot.Data)
}

func TestTestRunWithoutStepsOrScreenshot(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	runner := &scriptedRunner{result: &aichecks.RunResult{Status: "up", Screenshot: &checkerdef.Screenshot{}}}
	rec := postTestRun(t, NewHandler(runner, &config.Config{}), `{"type":"js","config":{"script":"x"}}`)

	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), `"steps":[]`, "steps is always an array")
	r.NotContains(rec.Body.String(), "screenshot", "an empty capture is not served")
	r.Zero(runner.timeout, "no timeout asks for the default")
}

func TestTestRunReportsRunnerFailures(t *testing.T) {
	t.Parallel()

	t.Run("an invalid script config is the caller's fault", func(t *testing.T) {
		t.Parallel()

		runner := &scriptedRunner{err: &checkerdef.ConfigError{Parameter: "script", Message: "too long"}}
		rec := postTestRun(t, NewHandler(runner, &config.Config{}), `{"type":"js","config":{"script":"x"}}`)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "too long")
	})

	t.Run("any other failure is internal", func(t *testing.T) {
		t.Parallel()

		runner := &scriptedRunner{err: errRunnerBoom}
		rec := postTestRun(t, NewHandler(runner, &config.Config{}), `{"type":"js","config":{"script":"x"}}`)

		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "INTERNAL_ERROR")
	})
}

// blockingRunner holds every run until release is closed, and reports each
// start on started.
type blockingRunner struct {
	started chan struct{}
	release chan struct{}
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{started: make(chan struct{}, 16), release: make(chan struct{})}
}

func (b *blockingRunner) Test(
	ctx context.Context, _ string, _, _ map[string]string, _ time.Duration,
) (*aichecks.RunResult, error) {
	b.started <- struct{}{}

	select {
	case <-b.release:
	case <-ctx.Done():
	}

	return &aichecks.RunResult{Status: "up"}, nil
}

func postAsOrg(t *testing.T, handler *Handler, orgUID string) *httptest.ResponseRecorder {
	t.Helper()

	ctx := context.WithValue(t.Context(), base.ContextKeyOrganization, &models.Organization{UID: orgUID})
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/orgs/x/checks/test-run",
		strings.NewReader(`{"type":"js","config":{"script":"return { status: 'up' };"}}`))
	rec := httptest.NewRecorder()

	require.NoError(t, handler.TestRun(rec, req))

	return rec
}

// TestTestRunPerOrgCap is the regression test of spec 2026-11-18-01: one org
// holding its test-run slot does not block another org.
func TestTestRunPerOrgCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	runner := newBlockingRunner()
	handler := NewHandler(runner, &config.Config{})

	// Org A tries to take every process slot at once: only MaxPerOrg runs
	// start, the others are refused at once. Before the fix, org A held all
	// maxConcurrent slots and org B got 429 below.
	var wg sync.WaitGroup

	codes := make(chan *httptest.ResponseRecorder, maxConcurrent)

	for range maxConcurrent {
		wg.Go(func() { codes <- postAsOrg(t, handler, "org-a") })
	}

	for range maxConcurrent - MaxPerOrg {
		select {
		case rec := <-codes:
			r.Equal(http.StatusTooManyRequests, rec.Code)
			r.Equal("5", rec.Header().Get("Retry-After"))
			r.Contains(rec.Body.String(), "per organization")
		case <-time.After(5 * time.Second):
			r.Fail("org A's extra runs were not refused")
		}
	}

	for range MaxPerOrg {
		<-runner.started
	}

	// Org B, with org A still holding its slot, runs.
	doneB := make(chan int, 1)

	go func() { doneB <- postAsOrg(t, handler, "org-b").Code }()

	select {
	case <-runner.started:
	case code := <-doneB:
		r.Failf("org B was refused", "status %d", code)
	}

	close(runner.release)
	r.Equal(http.StatusOK, <-doneB)

	wg.Wait()
	close(codes)

	for rec := range codes {
		r.Equal(http.StatusOK, rec.Code)
	}

	// Positive control: once its runs are over, org A runs again, and no busy
	// org is left behind in the map.
	r.Zero(handler.busyOrgs())
	r.Equal(http.StatusOK, postAsOrg(t, handler, "org-a").Code)
	<-runner.started
	r.Zero(handler.busyOrgs(), "the per-org map is empty after every run")
	r.Empty(handler.slots)
}

// budgetRunner reports whether the run's context asks browsers for the
// test-run budget.
type budgetRunner struct{ budget bool }

func (b *budgetRunner) Test(
	ctx context.Context, _ string, _, _ map[string]string, _ time.Duration,
) (*aichecks.RunResult, error) {
	b.budget = checkbrowser.IsTestRun(ctx)

	return &aichecks.RunResult{Status: "up"}, nil
}

func TestTestRunUsesTheBrowserTestRunBudget(t *testing.T) {
	t.Parallel()

	runner := &budgetRunner{}
	rec := postAsOrg(t, NewHandler(runner, &config.Config{}), "org-a")

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, runner.budget, "a test run's browsers count against the test-run budget")
}
