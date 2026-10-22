// Package agenttasks serves the worker-task routes a deported agent pulls its
// tasks from (spec 2026-10-22-02): claim, heartbeat, step, finish and fail,
// all authenticated with the agent's Ed25519 signed headers.
//
// Plain HTTP, never the WebSocket: the socket registry is per replica, a
// frame over 256 KiB or of an unknown type closes the agent connection, and an
// old server answers 404 here, which the agent reads as "nothing to do".
package agenttasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/agentauth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// MaxBodyBytes caps every request body (413 above).
const MaxBodyBytes = 1024 * 1024

// agentSlots is how many tasks one agent holds at once.
const agentSlots = 1

// errorCodeTaskGone is the code of a 410: the task is not held by this agent.
const errorCodeTaskGone base.ErrorCode = "TASK_GONE"

var errBodyTooLarge = errors.New("request body too large")

// Handler serves /api/v1/agent/tasks/*.
type Handler struct {
	base.HandlerBase
	svc    *workertasks.Service
	auth   *agentauth.Authenticator
	logger *slog.Logger
}

// NewHandler builds the handler over the worker-task service.
func NewHandler(svc *workertasks.Service, dbService db.Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
		auth:        agentauth.New(dbService, agents.NewStoredNonceGuard(dbService, db.ErrAgentNonceReplayed)),
		logger:      slog.Default().With("component", "agent_tasks"),
	}
}

// SetNonceGuard swaps the replay guard (tests).
func (h *Handler) SetNonceGuard(guard agents.NonceGuard) {
	h.auth.SetNonceGuard(guard)
}

// agentFor authenticates the request. It writes the refusal and returns nil
// when the caller may not use these routes: 401 for a bad signature or a
// revoked agent, 403 for a system agent (cloud workers already cover cloud
// regions).
func (h *Handler) agentFor(writer http.ResponseWriter, req *http.Request) (*models.Agent, error) {
	agent, err := h.auth.Authenticate(req)
	if err != nil {
		return nil, h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeInvalidToken, "Invalid agent signature")
	}

	if agent.IsSystem() {
		return nil, h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden,
			"System agents do not run worker tasks")
	}

	return agent, nil
}

func readBody(req *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(req.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if len(body) > MaxBodyBytes {
		return nil, errBodyTooLarge
	}

	return body, nil
}

// decode reads a capped JSON body into dest. An empty body leaves dest as is.
func (h *Handler) decode(writer http.ResponseWriter, req *http.Request, dest any) (bool, error) {
	body, err := readBody(req)
	if err != nil {
		return false, h.writeServiceError(writer, req, err)
	}

	if len(body) == 0 {
		return true, nil
	}

	if err := json.Unmarshal(body, dest); err != nil {
		return false, h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	return true, nil
}

// taskUID returns the path's task uid, or "" when it is not a uuid (such a
// task can never be held, so it reads as gone).
func taskUID(req *http.Request) string {
	uid := httpx.Param(req, "uid")
	if _, err := uuid.Parse(uid); err != nil {
		return ""
	}

	return uid
}

func (h *Handler) writeServiceError(writer http.ResponseWriter, req *http.Request, err error) error {
	switch {
	case errors.Is(err, workertasks.ErrTaskGone):
		return h.WriteError(writer, http.StatusGone, errorCodeTaskGone, "Task not held by this agent")
	case errors.Is(err, errBodyTooLarge), errors.Is(err, workertasks.ErrTooLarge):
		return h.WriteError(writer, http.StatusRequestEntityTooLarge, base.ErrorCodeValidationError,
			"Request exceeds the size limit")
	case errors.Is(err, workertasks.ErrInvalidState):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	default:
		return h.WriteInternalError(writer, req, err)
	}
}

// Claim handles POST /api/v1/agent/tasks/claim: 200 with a task, 204 when
// there is none. Kinds the server does not know are ignored.
func (h *Handler) Claim(writer http.ResponseWriter, req *http.Request) error {
	agent, err := h.agentFor(writer, req)
	if agent == nil {
		return err
	}

	var body workertasks.ClaimRequest
	if ok, decodeErr := h.decode(writer, req, &body); !ok {
		return decodeErr
	}

	claim, err := h.svc.Claim(req.Context(), workertasks.WorkerIdentity{
		UID:     agent.UID,
		Region:  agent.Region,
		OrgUID:  agent.OrgUID(),
		IsAgent: true,
		Kinds:   body.Kinds,
		Slots:   agentSlots,
	})
	if err != nil {
		return h.writeServiceError(writer, req, err)
	}

	if claim == nil {
		writer.WriteHeader(http.StatusNoContent)

		return nil
	}

	h.logger.DebugContext(req.Context(), "Agent claimed a worker task",
		"agent", agent.UID, "task", claim.UID, "kind", claim.Kind, "slice", claim.Slice)

	return h.WriteJSON(writer, http.StatusOK, claim)
}

// Heartbeat handles POST /api/v1/agent/tasks/:uid/heartbeat: 200 or 410.
func (h *Handler) Heartbeat(writer http.ResponseWriter, req *http.Request) error {
	agent, err := h.agentFor(writer, req)
	if agent == nil {
		return err
	}

	leaseUntil, err := h.svc.Heartbeat(req.Context(), taskUID(req), agent.UID)
	if err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, workertasks.HeartbeatResponse{LeaseUntil: leaseUntil})
}

// Step handles POST /api/v1/agent/tasks/:uid/step: 200, 410 or 413.
func (h *Handler) Step(writer http.ResponseWriter, req *http.Request) error {
	agent, err := h.agentFor(writer, req)
	if agent == nil {
		return err
	}

	var body workertasks.StepRequest
	if ok, decodeErr := h.decode(writer, req, &body); !ok {
		return decodeErr
	}

	if err := h.svc.Step(req.Context(), taskUID(req), agent.UID, body); err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

// Finish handles POST /api/v1/agent/tasks/:uid/finish: 200, 410 or 413. The
// body is the kind's result, validated by the kind.
func (h *Handler) Finish(writer http.ResponseWriter, req *http.Request) error {
	agent, err := h.agentFor(writer, req)
	if agent == nil {
		return err
	}

	body, err := readBody(req)
	if err != nil {
		return h.writeServiceError(writer, req, err)
	}

	if len(body) > 0 && !json.Valid(body) {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	if err := h.svc.Finish(req.Context(), taskUID(req), agent.UID, body); err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

// Fail handles POST /api/v1/agent/tasks/:uid/fail: 200 or 410.
func (h *Handler) Fail(writer http.ResponseWriter, req *http.Request) error {
	agent, err := h.agentFor(writer, req)
	if agent == nil {
		return err
	}

	var body workertasks.FailRequest
	if ok, decodeErr := h.decode(writer, req, &body); !ok {
		return decodeErr
	}

	if err := h.svc.FailSlice(req.Context(), taskUID(req), agent.UID, body.ErrorCode); err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

// Register mounts the routes on the /api/v1 group. They carry the agent's
// signed-header auth inline, so they never go on an authenticated group.
func (h *Handler) Register(api *httpx.Group) {
	api.POST("/agent/tasks/claim", h.Claim)
	api.POST("/agent/tasks/:uid/heartbeat", h.Heartbeat)
	api.POST("/agent/tasks/:uid/step", h.Step)
	api.POST("/agent/tasks/:uid/finish", h.Finish)
	api.POST("/agent/tasks/:uid/fail", h.Fail)
}
