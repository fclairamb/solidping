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
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

const (
	maxBodyBytes = 256 * 1024
	// maxConcurrent bounds the test runs a process executes at once: each may
	// hold a browser slot.
	maxConcurrent = 4
	// MaxPerOrg bounds the test runs one org has in flight at once (spec
	// 2026-11-18-01), so a single tenant can never hold every process slot.
	MaxPerOrg = 1
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
	slots  chan struct{}

	mu sync.Mutex
	// perOrg counts the runs in flight by org UID. An entry is deleted when
	// its count drops back to zero, so the map only holds busy orgs.
	perOrg map[string]int
}

// NewHandler builds the handler.
func NewHandler(runner Runner, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		runner:      runner,
		slots:       make(chan struct{}, maxConcurrent),
		perOrg:      map[string]int{},
	}
}

// errOrgBusy and errProcessBusy are the two reasons acquire refuses a run.
var (
	errOrgBusy     = errors.New("org at its test-run limit")
	errProcessBusy = errors.New("every test-run slot is busy")
)

// acquire reserves one of the org's MaxPerOrg slots and one of the process's
// maxConcurrent slots, without waiting. The returned release frees both.
func (h *Handler) acquire(orgUID string) (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.perOrg[orgUID] >= MaxPerOrg {
		return nil, errOrgBusy
	}

	select {
	case h.slots <- struct{}{}:
	default:
		return nil, errProcessBusy
	}

	h.perOrg[orgUID]++

	var once sync.Once

	return func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()

			h.perOrg[orgUID]--
			if h.perOrg[orgUID] <= 0 {
				delete(h.perOrg, orgUID)
			}

			<-h.slots
		})
	}, nil
}

// busyOrgs reports how many orgs have a run in flight. For tests.
func (h *Handler) busyOrgs() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.perOrg)
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
