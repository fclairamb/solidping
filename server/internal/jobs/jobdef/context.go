// Package jobdef defines the core job system types and interfaces.
package jobdef

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// JobContext provides all dependencies needed by a job during execution.
type JobContext struct {
	// OrganizationUID is the organization this job belongs to (nil for system jobs)
	OrganizationUID *string

	// Job is the job model from database
	Job *models.Job

	// Config is the parsed job configuration
	Config json.RawMessage

	// Services provides access to all application services
	Services *services.Registry

	// DB provides direct database access
	DB *bun.DB

	// DBService provides database service interface
	DBService db.Service

	// Tx is the active transaction (if any)
	Tx bun.Tx

	// AppConfig is the application configuration
	AppConfig *config.Config

	// Logger is the structured logger for this job execution
	// It includes a "jobUid" attribute for log correlation
	Logger *slog.Logger
}

// ClockNow returns the current time using the injected clock if available,
// or falls back to time.Now(). This prevents nil-pointer panics when Services
// is not wired (e.g. in unit tests).
func (jctx *JobContext) ClockNow() time.Time {
	if jctx.Services != nil && jctx.Services.Clock != nil {
		return jctx.Services.Clock.Now()
	}

	return time.Now()
}
