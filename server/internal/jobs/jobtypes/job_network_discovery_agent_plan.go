package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/discoverytask"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// agentPlanInsertBatch bounds the tasks one INSERT carries.
const agentPlanInsertBatch = 500

var errAgentPlanNoRegion = errors.New("network discovery agent plan requires a region")

// NetworkDiscoveryAgentPlanJobDefinition is the factory for region-bound LAN
// scans (spec 2026-10-22-04).
type NetworkDiscoveryAgentPlanJobDefinition struct{}

// Type returns the job type.
func (d *NetworkDiscoveryAgentPlanJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeNetworkDiscoveryAgentPlan
}

// CreateJobRun validates {cidrs, ports, region}.
func (d *NetworkDiscoveryAgentPlanJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg discoverytask.PlanConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("invalid network discovery agent plan config: %w", err)
	}

	if err := disc.ValidatePlanCIDRs(cfg.CIDRs); err != nil {
		return nil, err
	}

	if cfg.Region == "" {
		return nil, errAgentPlanNoRegion
	}

	return &NetworkDiscoveryAgentPlanJobRun{config: cfg}, nil
}

// NetworkDiscoveryAgentPlanJobRun is one region-bound LAN scan.
type NetworkDiscoveryAgentPlanJobRun struct {
	config discoverytask.PlanConfig
}

// Run splits the CIDRs into ≤MaxAddresses chunks and inserts one pending
// "discovery" worker task per chunk, in the scan's region, under this plan.
// It never scans and never dials: the agents do.
func (r *NetworkDiscoveryAgentPlanJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	if jctx.OrganizationUID == nil {
		return errNoOrganizationUID
	}

	orgUID := *jctx.OrganizationUID
	planUID := jctx.Job.UID

	chunks, err := disc.SplitCIDRs(r.config.CIDRs, disc.MaxAddresses)
	if err != nil {
		return fmt.Errorf("split cidrs: %w", err)
	}

	ports := r.config.Ports
	if len(ports) == 0 {
		ports = disc.DefaultPortList()
	}

	tasks := make([]*models.WorkerTask, 0, len(chunks))

	for i := range chunks {
		task := models.NewWorkerTask(orgUID, discoverytask.Kind, r.config.Region)
		task.JobUID = &planUID
		task.Payload = models.JSONMap{"cidrs": chunks[i], "ports": ports}
		tasks = append(tasks, task)
	}

	err = jctx.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for start := 0; start < len(tasks); start += agentPlanInsertBatch {
			batch := tasks[start:min(start+agentPlanInsertBatch, len(tasks))]
			if _, insertErr := tx.NewInsert().Model(&batch).Exec(ctx); insertErr != nil {
				return fmt.Errorf("insert discovery tasks: %w", insertErr)
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	jctx.Job.Output = map[string]any{"totalChunks": len(chunks)}

	jctx.Logger.InfoContext(ctx, "Agent network discovery scheduled",
		"cidrs", r.config.CIDRs, "region", r.config.Region, "chunk_count", len(chunks),
		"org_uid", orgUID, "plan_job_uid", planUID)

	return nil
}
