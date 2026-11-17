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
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

const (
	maxBodyBytes = 256 * 1024
	// maxConcurrent bounds the test runs a process executes at once: each may
	// hold a browser slot.
	maxConcurrent = 4
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
}

// NewHandler builds the handler.
func NewHandler(runner Runner, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		runner:      runner,
		slots:       make(chan struct{}, maxConcurrent),
	}
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

	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		writer.Header().Set("Retry-After", "5")

		return h.WriteError(writer, http.StatusTooManyRequests, base.ErrorCodeRateLimited,
			"Too many test runs in progress")
	}

	result, err := h.runner.Test(req.Context(), payload.Config.Script, payload.Config.Env, payload.Config.Secrets, timeout)
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
