package incidents

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// aiRepairBounce collapses the repair jobs a run of drifting results queues
// into one per check.
const aiRepairBounce = 30 * time.Second

// unrepairedChecks remembers the drifting checks already reported as
// unrepairable on this process, so the warning is logged once per check.
var unrepairedChecks sync.Map //nolint:gochecknoglobals // process-wide log dedup

// queueAIRepair queues an ai_repair job when an AI-authored js check reports
// a drift-class failure (spec 2026-10-03-07). Only the cheap, local gate runs
// here, on the hot result path; the job checks the rest (consecutive
// failures, target health, rate limits). Best-effort.
func (s *Service) queueAIRepair(ctx context.Context, check *models.Check, result *models.Result) {
	if s.jobsSvc == nil || result.Status == nil ||
		check.Type != string(checkerdef.CheckTypeJS) || !aichecks.WantsRepair(check.Config) {
		return
	}

	if aichecks.ClassifyFailure(models.ResultStatus(*result.Status), result.Output) != aichecks.FailureDrift {
		return
	}

	if !aichecks.Enabled() {
		if _, seen := unrepairedChecks.LoadOrStore(check.UID, struct{}{}); !seen {
			slog.WarnContext(ctx, "AI-authored check drifts but no AI provider is configured (SP_AI_PROVIDER): "+
				"it will not be repaired", "check_uid", check.UID)
		}

		return
	}

	config, err := json.Marshal(map[string]string{"checkUid": check.UID})
	if err != nil {
		return
	}

	bounce := aiRepairBounce
	if _, err := s.jobsSvc.CreateJob(ctx, check.OrganizationUID, string(jobdef.JobTypeAIRepair), config,
		&jobsvc.JobOptions{BounceDelay: &bounce}); err != nil {
		slog.WarnContext(ctx, "Failed to queue the AI repair job", "error", err, "check_uid", check.UID)
	}
}
