package workers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler provides HTTP handlers for the edge worker API.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new workers handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// Register handles POST /api/v1/workers/register.
func (h *Handler) Register(
	writer http.ResponseWriter, req bunrouter.Request,
) error {
	var body RegisterRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(
			writer, http.StatusBadRequest,
			base.ErrorCodeValidationError,
			"Invalid request body",
		)
	}

	if body.Slug == "" || body.Name == "" {
		return h.WriteError(
			writer, http.StatusBadRequest,
			base.ErrorCodeValidationError,
			"slug and name are required",
		)
	}

	resp, err := h.svc.Register(req.Context(), &body)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// heartbeatBody is the JSON body for POST /workers/heartbeat.
type heartbeatBody struct {
	WorkerUID string `json:"workerUid"`
}

// Heartbeat handles POST /api/v1/workers/heartbeat.
func (h *Handler) Heartbeat(
	writer http.ResponseWriter, req bunrouter.Request,
) error {
	token := extractBearerToken(req.Request)

	workerUID, err := h.svc.ValidateToken(req.Context(), token)
	if err != nil {
		return h.handleAuthError(writer, err)
	}

	// Also accept workerUid in body for explicit targeting
	var body heartbeatBody
	if req.Body != nil {
		_ = json.NewDecoder(req.Body).Decode(&body)
	}

	uid := workerUID
	if body.WorkerUID != "" {
		uid = body.WorkerUID
	}

	if err := h.svc.Heartbeat(req.Context(), uid); err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(
		writer, http.StatusOK, map[string]string{"status": "ok"},
	)
}

// claimJobsBody is the JSON body for POST /workers/claim-jobs.
type claimJobsBody struct {
	WorkerUID string  `json:"workerUid"`
	Region    *string `json:"region,omitempty"`
	Limit     int     `json:"limit"`
	MaxAhead  string  `json:"maxAhead"`
}

// ClaimJobs handles POST /api/v1/workers/claim-jobs.
func (h *Handler) ClaimJobs(
	writer http.ResponseWriter, req bunrouter.Request,
) error {
	token := extractBearerToken(req.Request)

	if _, err := h.svc.ValidateToken(
		req.Context(), token,
	); err != nil {
		return h.handleAuthError(writer, err)
	}

	var body claimJobsBody
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(
			writer, http.StatusBadRequest,
			base.ErrorCodeValidationError,
			"Invalid request body",
		)
	}

	if body.Limit <= 0 {
		body.Limit = 5
	}

	maxAhead := 5 * time.Minute
	if body.MaxAhead != "" {
		parsed, err := time.ParseDuration(body.MaxAhead)
		if err == nil {
			maxAhead = parsed
		}
	}

	resp, err := h.svc.ClaimJobs(req.Context(), &ClaimJobsRequest{
		WorkerUID: body.WorkerUID,
		Region:    body.Region,
		Limit:     body.Limit,
		MaxAhead:  maxAhead,
	})
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// SubmitResult handles POST /api/v1/workers/submit-result.
func (h *Handler) SubmitResult(
	writer http.ResponseWriter, req bunrouter.Request,
) error {
	token := extractBearerToken(req.Request)

	if _, err := h.svc.ValidateToken(
		req.Context(), token,
	); err != nil {
		return h.handleAuthError(writer, err)
	}

	var body SubmitResultRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(
			writer, http.StatusBadRequest,
			base.ErrorCodeValidationError,
			"Invalid request body",
		)
	}

	if body.JobUID == "" || body.WorkerUID == "" {
		return h.WriteError(
			writer, http.StatusBadRequest,
			base.ErrorCodeValidationError,
			"jobUid and workerUid are required",
		)
	}

	resp, err := h.svc.SubmitResult(req.Context(), &body)
	if err != nil {
		if errors.Is(err, ErrJobNotFound) {
			return h.WriteError(
				writer, http.StatusNotFound,
				base.ErrorCodeNotFound,
				"Check job not found",
			)
		}

		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// handleAuthError writes an appropriate authentication error
// response.
func (h *Handler) handleAuthError(
	writer http.ResponseWriter, err error,
) error {
	if errors.Is(err, ErrMissingToken) {
		return h.WriteError(
			writer, http.StatusUnauthorized,
			base.ErrorCodeNoToken,
			"Worker token is required",
		)
	}

	return h.WriteError(
		writer, http.StatusUnauthorized,
		base.ErrorCodeInvalidToken,
		"Invalid worker token",
	)
}

// extractBearerToken extracts the bearer token from the Authorization
// header.
func extractBearerToken(req *http.Request) string {
	auth := req.Header.Get("Authorization")
	if auth == "" {
		return ""
	}

	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return ""
	}

	return auth[len(prefix):]
}
