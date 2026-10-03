// Package aichecks serves the AI authoring endpoints of js checks (spec
// 2026-10-03-07): POST /orgs/:org/checks/ai/contract turns a prompt into a
// contract to confirm, POST /orgs/:org/checks/ai/generate writes and tests the
// script. Nothing is saved here: the dashboard saves the returned config
// through the regular create path. Both answer 404 when no AI provider is
// configured.
package aichecks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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

	resp, err := h.svc.Generate(ctx, orgUID, &body)
	if err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

func (h *Handler) writeServiceError(writer http.ResponseWriter, req *http.Request, err error) error {
	var genErr *svc.GenerationError
	if errors.As(err, &genErr) {
		return h.WriteJSON(writer, http.StatusUnprocessableEntity, GenerationFailedResponse{
			Title:      "No script passed its test run",
			Code:       "AI_GENERATION_FAILED",
			Detail:     genErr.Error(),
			LastScript: genErr.LastScript,
			LastRun:    genErr.LastRun,
			Turns:      genErr.Turns,
		})
	}

	var apiErr *ai.APIError

	switch {
	case errors.Is(err, svc.ErrDisabled):
		return h.notEnabled(writer)
	case errors.Is(err, svc.ErrBudgetExceeded):
		return h.WriteError(writer, http.StatusTooManyRequests, base.ErrorCodeQuotaExceeded, err.Error())
	case errors.Is(err, svc.ErrPromptRequired), errors.Is(err, svc.ErrContractRequired):
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
