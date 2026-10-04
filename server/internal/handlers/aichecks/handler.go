// Package aichecks serves the AI authoring endpoints of js checks (spec
// 2026-10-03-07): POST /orgs/:org/checks/ai/contract turns a prompt into a
// contract to confirm, POST /orgs/:org/checks/ai/generate writes and tests the
// script. Nothing is saved here: the dashboard saves the returned config
// through the regular create path. Both answer 404 when no AI provider is
// configured. A generate request sent with Accept: application/x-ndjson gets
// its progress streamed, one JSON object per line, ending with a "result" or
// an "error" line.
package aichecks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/ai"
	svc "github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

const (
	maxBodyBytes = 256 * 1024
	// generateBudget bounds one generation end to end.
	generateBudget = 10 * time.Minute
	// ndjsonContentType is the streamed generation's media type.
	ndjsonContentType = "application/x-ndjson"
	// streamHeartbeat keeps a streamed generation's connection busy while a
	// reasoning model thinks for a minute without any event, so no proxy
	// drops it as idle.
	streamHeartbeat = 15 * time.Second
)

// Stream line types besides the svc.Progress ones.
const (
	streamPing   = "ping"
	streamResult = "result"
	streamError  = "error"
)

// Handler serves the AI authoring endpoints.
type Handler struct {
	base.HandlerBase
	svc *svc.Service
	db  db.Service
}

// NewHandler builds the handler. A disabled service answers 404.
func NewHandler(service *svc.Service, dbService db.Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: service, db: dbService}
}

// ContractRequest is the contract step's body.
type ContractRequest struct {
	Prompt string `json:"prompt"`
}

// GenerationFailedResponse is a 422 with the last attempt.
type GenerationFailedResponse struct {
	Title      string         `json:"title"`
	Code       string         `json:"code"`
	Detail     string         `json:"detail"`
	LastScript string         `json:"lastScript,omitempty"`
	LastRun    *svc.RunResult `json:"lastRun,omitempty"`
	Turns      int            `json:"turns"`
	// Explanation is the model's last message, often why it gave up.
	Explanation string `json:"explanation,omitempty"`
}

// StreamLine is the last line of a streamed generation: the response the
// plain endpoint would have sent, with its HTTP status for an error.
type StreamLine struct {
	Type   string                `json:"type"`
	Result *svc.GenerateResponse `json:"result,omitempty"`
	// HTTPStatus and Response are the error the plain call would have
	// answered.
	HTTPStatus int             `json:"httpStatus,omitempty"`
	Response   json.RawMessage `json:"response,omitempty"`
}

func (h *Handler) notEnabled(writer http.ResponseWriter) error {
	return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, svc.ErrDisabled.Error())
}

func (h *Handler) orgUID(writer http.ResponseWriter, req *http.Request) (string, bool, error) {
	org, err := h.db.GetOrganizationBySlug(req.Context(), httpx.Param(req, "org"))
	if err != nil || org == nil {
		return "", false, h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
			"Organization not found")
	}

	return org.UID, true, nil
}

func decode(req *http.Request, into any) error {
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBodyBytes))
	if err != nil {
		return err
	}

	return json.Unmarshal(body, into)
}

// Contract handles POST /orgs/:org/checks/ai/contract.
func (h *Handler) Contract(writer http.ResponseWriter, req *http.Request) error {
	if !h.svc.Enabled() {
		return h.notEnabled(writer)
	}

	orgUID, ok, err := h.orgUID(writer, req)
	if !ok {
		return err
	}

	var body ContractRequest
	if decodeErr := decode(req, &body); decodeErr != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	resp, err := h.svc.Contract(req.Context(), orgUID, body.Prompt)
	if err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// Generate handles POST /orgs/:org/checks/ai/generate.
func (h *Handler) Generate(writer http.ResponseWriter, req *http.Request) error {
	if !h.svc.Enabled() {
		return h.notEnabled(writer)
	}

	orgUID, ok, err := h.orgUID(writer, req)
	if !ok {
		return err
	}

	var body svc.GenerateRequest
	if decodeErr := decode(req, &body); decodeErr != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	ctx, cancel := context.WithTimeout(req.Context(), generateBudget)
	defer cancel()

	if wantsStream(req) {
		h.generateStream(ctx, writer, req, orgUID, &body)

		return nil
	}

	resp, err := h.svc.Generate(ctx, orgUID, &body)
	if err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

func wantsStream(req *http.Request) bool {
	for _, part := range strings.Split(req.Header.Get("Accept"), ",") {
		if mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(part)); err == nil &&
			mediaType == ndjsonContentType {
			return true
		}
	}

	return false
}

// ndjsonStream writes one JSON object per line and flushes each, safe for
// the generation and its heartbeat to share.
type ndjsonStream struct {
	mu      sync.Mutex
	writer  http.ResponseWriter
	flusher http.Flusher
}

func newNDJSONStream(writer http.ResponseWriter) *ndjsonStream {
	flusher, _ := writer.(http.Flusher)

	writer.Header().Set("Content-Type", ndjsonContentType)
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)

	return &ndjsonStream{writer: writer, flusher: flusher}
}

func (s *ndjsonStream) write(line any) error {
	raw, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("encoding a stream line: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, _ = s.writer.Write(append(raw, '\n'))

	if s.flusher != nil {
		s.flusher.Flush()
	}

	return nil
}

// heartbeat writes a ping line every streamHeartbeat until the returned stop
// function is called; stop returns once the last ping is written.
func (s *ndjsonStream) heartbeat() func() {
	done := make(chan struct{})
	exited := make(chan struct{})

	go func() {
		defer close(exited)

		ticker := time.NewTicker(streamHeartbeat)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = s.write(StreamLine{Type: streamPing})
			}
		}
	}()

	return func() {
		close(done)
		<-exited
	}
}

// generateStream runs a generation and streams its progress as NDJSON. The
// status is 200 once the stream starts: an error is the last line, carrying
// the status and body the plain endpoint would have answered.
func (h *Handler) generateStream(
	ctx context.Context, writer http.ResponseWriter, req *http.Request, orgUID string, body *svc.GenerateRequest,
) {
	stream := newNDJSONStream(writer)
	body.OnProgress = func(event svc.Progress) { _ = stream.write(event) }

	stop := stream.heartbeat()
	resp, err := h.svc.Generate(ctx, orgUID, body)

	stop()

	last := StreamLine{Type: streamResult, Result: resp}
	if err != nil {
		last = h.errorLine(req, err)
	}

	// The last line always goes out: a stream that just stops reads as a
	// network failure on the dashboard.
	if writeErr := stream.write(last); writeErr != nil {
		_ = stream.write(h.errorLine(req, writeErr))
	}
}

// errorLine is the error the plain endpoint would answer, as a stream line.
func (h *Handler) errorLine(req *http.Request, err error) StreamLine {
	recorder := httptest.NewRecorder()
	_ = h.writeServiceError(recorder, req, err)

	return StreamLine{Type: streamError, HTTPStatus: recorder.Code, Response: recorder.Body.Bytes()}
}

func (h *Handler) writeServiceError(writer http.ResponseWriter, req *http.Request, err error) error {
	var genErr *svc.GenerationError
	if errors.As(err, &genErr) {
		return h.WriteJSON(writer, http.StatusUnprocessableEntity, GenerationFailedResponse{
			Title:       "No script passed its test run",
			Code:        "AI_GENERATION_FAILED",
			Detail:      genErr.Detail(),
			LastScript:  genErr.LastScript,
			LastRun:     genErr.LastRun,
			Turns:       genErr.Turns,
			Explanation: genErr.Explanation,
		})
	}

	var apiErr *ai.APIError

	switch {
	case errors.Is(err, svc.ErrDisabled):
		return h.notEnabled(writer)
	case errors.Is(err, svc.ErrBudgetExceeded):
		return h.WriteError(writer, http.StatusTooManyRequests, base.ErrorCodeQuotaExceeded, err.Error())
	case errors.Is(err, svc.ErrPromptRequired), errors.Is(err, svc.ErrContractRequired),
		errors.Is(err, svc.ErrMissingSecret):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	case errors.Is(err, svc.ErrCheckNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, err.Error())
	case errors.Is(err, svc.ErrNoContract):
		return h.WriteError(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, err.Error())
	case errors.As(err, &apiErr):
		return h.WriteErrorErr(writer, req, http.StatusBadGateway, base.ErrorCodeInternalError,
			"The AI provider refused the request", err)
	}

	var cfgErr *checkerdef.ConfigError
	if errors.As(err, &cfgErr) {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	}

	return h.WriteInternalError(writer, req, err)
}
