package jobtypes

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// errAIRepairCheckRequired is an ai_repair job without a check.
var errAIRepairCheckRequired = errors.New("ai_repair: checkUid is required")

// AIRepairJobDefinition is the factory of the ai_repair job (spec
// 2026-10-03-07).
type AIRepairJobDefinition struct{}

// Type returns the ai_repair job type.
func (d *AIRepairJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeAIRepair
}

// AIRepairJobConfig names the check to look at.
type AIRepairJobConfig struct {
	CheckUID string `json:"checkUid"`
}

// CreateJobRun builds an executable instance.
func (d *AIRepairJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg AIRepairJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, err
	}

	if cfg.CheckUID == "" {
		return nil, errAIRepairCheckRequired
	}

	return &AIRepairJobRun{config: cfg}, nil
}

// AIRepairJobRun is one repair evaluation.
type AIRepairJobRun struct {
	config AIRepairJobConfig
}

// Run evaluates the gates and, when they hold, attempts the repair. A
// process without an AI provider does nothing.
func (r *AIRepairJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	if jctx.Services == nil || jctx.Services.AIRepair == nil || jctx.OrganizationUID == nil {
		return nil
	}

	return jctx.Services.AIRepair.RepairCheck(ctx, *jctx.OrganizationUID, r.config.CheckUID)
}
