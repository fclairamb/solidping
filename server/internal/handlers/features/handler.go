// Package features exposes a minimal feature-flag endpoint so the frontend
// can decide which UI elements to render. Today only the bug-report icon
// looks at it; the package will grow as more conditional features land.
package features

import (
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler returns the active feature flags for the frontend.
type Handler struct {
	base.HandlerBase
	cfg *config.Config
}

// NewHandler constructs a Handler.
func NewHandler(cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		cfg:         cfg,
	}
}

// FeaturesResponse is the JSON shape returned by GET /api/v1/features.
type FeaturesResponse struct {
	BugReport bool `json:"bugReport"`
}

// GetFeatures handles GET /api/v1/features (auth required upstream).
func (h *Handler) GetFeatures(writer http.ResponseWriter, _ bunrouter.Request) error {
	return h.WriteJSON(writer, http.StatusOK, FeaturesResponse{
		BugReport: h.cfg.App.EnableBugReport,
	})
}
