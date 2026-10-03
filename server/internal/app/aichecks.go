package app

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/ai"
	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// buildAIChecks wires AI-authored js checks (spec 2026-10-03-07). With no
// provider configured the service is disabled: the endpoints answer 404, no
// repair job is queued, and the script tools (MCP) still work.
func buildAIChecks(
	cfg *config.Config, dbService db.Service, checksSvc *checks.Service,
	entSvc *entitlements.Service, creds credentials.Service, svcList *services.Registry,
) *aichecks.Service {
	client, err := ai.New(&cfg.AI, &http.Client{})
	if err != nil {
		if !errors.Is(err, ai.ErrDisabled) {
			slog.Warn("AI-authored checks disabled", "error", err)
		}

		client = nil
	}

	svc := aichecks.NewService(aichecks.Options{
		Client:       client,
		DB:           dbService,
		Checks:       checksSvc,
		Entitlements: entSvc,
		Credentials:  creds,
		// The guard is installed after NewServer (installEgressGuard), so it
		// is read at call time.
		Runner: &aichecks.Runner{Guard: func() *egress.Guard { return svcList.EgressGuard }},
	})
	svc.SetMailer(&aichecks.JobsMailer{Jobs: svcList.Jobs})
	svc.SetBaseURL(cfg.Server.BaseURL + config.DashboardBasePath)

	aichecks.SetEnabled(client != nil)

	if client != nil {
		svcList.AIRepair = svc

		slog.Info("AI-authored checks enabled", "provider", cfg.AI.Provider, "model", cfg.AI.Model)
	}

	return svc
}
