// Package checktestrun serves POST /orgs/:org/checks/test-run: it executes a
// check definition that is not saved yet, once, and returns the result with
// its named steps (spec 2026-11-17-04). Nothing is stored and nothing is
// notified: no check, no result row, no incident, no alert.
package checktestrun

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/testruns"
)

const (
	maxBodyBytes = 256 * 1024
	// maxConcurrent bounds the test runs a process executes at once: each may
	// hold a browser slot. Shared with the MCP probes through testruns.
	maxConcurrent = testruns.MaxConcurrent
	// MaxPerOrg bounds the test runs one org has in flight at once (spec
	// 2026-11-18-01), so a single tenant can never hold every process slot.
	MaxPerOrg = testruns.MaxPerOrg
	// retryAfterSeconds is the Retry-After of a refused run.
	retryAfterSeconds = "5"
)

// Runner executes a script once. *aichecks.Runner implements it.
type Runner interface {
	Test(
		ctx context.Context, script string, env, secrets map[string]string, timeout time.Duration,
	) (*aichecks.RunResult, error)
}

// Handler serves the test-run endpoint.
type Handler struct {
	base.HandlerBase

	runner Runner
	// limiter is the process-wide admission control, shared with the MCP
	// probes and the AI authoring loop (spec 2026-11-19-03).
	limiter *testruns.Limiter
	// slots is limiter's process semaphore, kept for tests.
	slots chan struct{}
}

// NewHandler builds the handler with a limiter of its own.
func NewHandler(runner Runner, cfg *config.Config) *Handler {
	return NewHandlerWithLimiter(runner, cfg, testruns.New())
}

// NewHandlerWithLimiter builds the handler on a shared limiter, so its runs
// count against the same caps as every other on-demand run of the process.
func NewHandlerWithLimiter(runner Runner, cfg *config.Config, limiter *testruns.Limiter) *Handler {
	if limiter == nil {
		limiter = testruns.New()
	}

	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		runner:      runner,
		limiter:     limiter,
		slots:       limiter.Slots(),
	}
}

// errOrgBusy is the per-org refusal of acquire.
var errOrgBusy = testruns.ErrOrgBusy

// acquire reserves one of the org's MaxPerOrg slots and one of the process's
// maxConcurrent slots, without waiting. The returned release frees both.
func (h *Handler) acquire(orgUID string) (func(), error) {
	return h.limiter.Acquire(orgUID)
}

// busyOrgs reports how many orgs have a run in flight. For tests.
func (h *Handler) busyOrgs() int {
	return h.limiter.BusyOrgs()
}

// orgUID is the org RequireOrgAccess resolved, or "" outside the real chain.
func orgUID(req *http.Request) string {
	if org, ok := middleware.GetOrganizationFromContext(req.Context()); ok && org != nil {
		return org.UID
	}

	return ""
}

// Request is the check definition to run. Only `js` checks can be tried.
type Request struct {
	Type   string        `json:"type"`
	Config RequestConfig `json:"config"`
}

// RequestConfig is the part of a js check's config a run needs.
type RequestConfig struct {
	Script  string            `json:"script"`
	Env     map[string]string `json:"env,omitempty"`
	Secrets map[string]string `json:"secrets,omitempty"`
	Timeout string            `json:"timeout,omitempty"`
}

// ScreenshotPayload is the capture the run kept.
type ScreenshotPayload struct {
	Format string `json:"format"`
	Data   string `json:"data"`
}

// Response is the run's outcome.
type Response struct {
	Status     string             `json:"status"`
	DurationMs int64              `json:"durationMs"`
	Message    string             `json:"message,omitempty"`
	Output     map[string]any     `json:"output,omitempty"`
	Metrics    map[string]any     `json:"metrics,omitempty"`
	Steps      []any              `json:"steps"`
	Screenshot *ScreenshotPayload `json:"screenshot,omitempty"`
}

// TestRun handles POST /api/v1/orgs/:org/checks/test-run.
//
// The script runs synchronously on the API node itself: one region = the API
// node's. There is no region selection for a test run.
func (h *Handler) TestRun(writer http.ResponseWriter, req *http.Request) error {
	// The per-org runs-per-minute allowance, shared with the MCP probes and
	// counted before the body is read: an invalid body still costs a run
	// (spec 2026-11-19-03).
	if !h.limiter.Allow(orgUID(req)) {
		writer.Header().Set("Retry-After", retryAfterSeconds)

		return h.WriteError(writer, http.StatusTooManyRequests, base.ErrorCodeRateLimited, fmt.Sprintf(
			"Too many test runs: this organization is limited to %d runs per minute", testruns.RunsPerMinute))
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, maxBodyBytes))
	if err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid body")
	}

	var payload Request
	if unmarshalErr := json.Unmarshal(body, &payload); unmarshalErr != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	if payload.Type != string(checkerdef.CheckTypeJS) {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Only js checks can be test-run")
	}

	if strings.TrimSpace(payload.Config.Script) == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "script is required")
	}

	var timeout time.Duration

	if payload.Config.Timeout != "" {
		parsed, parseErr := time.ParseDuration(payload.Config.Timeout)
		if parseErr != nil || parsed < 0 {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid timeout")
		}

		timeout = parsed
	}

	release, err := h.acquire(orgUID(req))
	if err != nil {
		writer.Header().Set("Retry-After", retryAfterSeconds)

		message := "Too many test runs in progress"
		if errors.Is(err, errOrgBusy) {
			message = fmt.Sprintf(
				"This organization already has a test run in progress (limit: %d at a time per organization)", MaxPerOrg)
		}

		return h.WriteError(writer, http.StatusTooManyRequests, base.ErrorCodeRateLimited, message)
	}

	defer release()

	// Every browser the script opens counts against the test-run budget, so
	// scheduled browser checks on this node always keep part of the pool.
	ctx := checkbrowser.WithTestRunBudget(req.Context())

	result, err := h.runner.Test(ctx, payload.Config.Script, payload.Config.Env, payload.Config.Secrets, timeout)
	if err != nil {
		var cfgErr *checkerdef.ConfigError
		if errors.As(err, &cfgErr) {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
		}

		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, buildResponse(result))
}

func buildResponse(result *aichecks.RunResult) *Response {
	out := &Response{
		Status:     result.Status,
		DurationMs: result.DurationMs,
		Output:     result.Output,
		Metrics:    result.Metrics,
		Steps:      []any{},
	}

	if steps, ok := result.Output[checkjs.OutputKeySteps].([]any); ok {
		out.Steps = steps
	}

	if msg, ok := result.Output["error"].(string); ok {
		out.Message = msg
	}

	if shot := result.Screenshot; shot != nil && len(shot.Image) > 0 {
		out.Screenshot = &ScreenshotPayload{
			Format: string(shot.Format),
			Data:   base64.StdEncoding.EncodeToString(shot.Image),
		}
	}

	return out
}
