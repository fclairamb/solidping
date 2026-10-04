package checkrunnow

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Handler serves POST /api/v1/orgs/:org/checks/:checkUid/run-now.
type Handler struct {
	base.HandlerBase

	svc *Service
}

// NewHandler builds the handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: svc}
}

// RunNow runs the check once, now, in every region. 200 with the per-region
// status; the results arrive through the normal path.
func (h *Handler) RunNow(writer http.ResponseWriter, req *http.Request) error {
	resp, err := h.svc.RunNow(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "checkUid"))
	if err != nil {
		return h.writeError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

func (h *Handler) writeError(writer http.ResponseWriter, req *http.Request, err error) error {
	var limited *RateLimitedError

	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found")
	case errors.Is(err, ErrNoScheduledJob):
		return h.WriteErrorErr(writer, req, http.StatusConflict, base.ErrorCodeConflict,
			"The check has no scheduled run", err)
	case errors.As(err, &limited):
		seconds := max(int(limited.RetryAfter.Round(time.Second).Seconds()), 1)

		writer.Header().Set("Retry-After", strconv.Itoa(seconds))

		return h.WriteErrorErr(writer, req, http.StatusTooManyRequests, base.ErrorCodeRateLimited,
			"Too many runs requested", err)
	default:
		return h.WriteInternalError(writer, req, err)
	}
}
