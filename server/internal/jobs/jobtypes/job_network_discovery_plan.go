package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// errNoJobService is returned when the plan runner cannot reach the job service
// it needs to schedule child jobs.
var errNoJobService = errors.New("network discovery plan job requires the job service")

// NetworkDiscoveryPlanJobDefinition is the factory for network discovery plan jobs.
type NetworkDiscoveryPlanJobDefinition struct{}

// Type returns the job type for network discovery plan jobs.
func (d *NetworkDiscoveryPlanJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeNetworkDiscoveryPlan
}

// CreateJobRun creates a new network discovery plan run from the given config.
func (d *NetworkDiscoveryPlanJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg disc.Config
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("invalid network discovery plan config: %w", err)
	}

	if err := disc.ValidatePlanCIDRs(cfg.CIDRs); err != nil {
		return nil, err
	}

	return &NetworkDiscoveryPlanJobRun{config: cfg}, nil
}

// NetworkDiscoveryPlanJobRun is an executable network discovery plan instance.
type NetworkDiscoveryPlanJobRun struct {
	config disc.Config
}

// Run splits the requested CIDRs into ≤MaxAddresses chunks and schedules one
// network_discovery child job per chunk. Each child carries this plan's UID as
// parentJobUid (so its hosts roll up under the plan) plus the inherited ports /
// timeout / concurrency. The total chunk count is written to the plan's Output as
// the progress denominator, then the plan completes success — scheduling rows is
// all it does, so it is fast.
func (r *NetworkDiscoveryPlanJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.OrganizationUID == nil {
		return errNoOrganizationUID
	}

	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return errNoJobService
	}

	orgUID := *jctx.OrganizationUID
	planUID := jctx.Job.UID
	jobSvc := jctx.Services.Jobs

	chunks, err := disc.SplitCIDRs(r.config.CIDRs, disc.MaxAddresses)
	if err != nil {
		return fmt.Errorf("split cidrs: %w", err)
	}

	log.InfoContext(ctx, "Planning network discovery fan-out",
		"cidrs", r.config.CIDRs,
		"chunk_count", len(chunks),
		"org_uid", orgUID,
		"plan_job_uid", planUID,
	)

	for i := range chunks {
		childCfg := disc.Config{
			CIDRs:        chunks[i],
			Ports:        r.config.Ports,
			Concurrency:  r.config.Concurrency,
			Timeout:      r.config.Timeout,
			ParentJobUID: planUID,
		}

		childBytes, marshalErr := json.Marshal(childCfg)
		if marshalErr != nil {
			return fmt.Errorf("marshal child config: %w", marshalErr)
		}

		if _, createErr := jobSvc.CreateJob(
			ctx, orgUID, string(jobdef.JobTypeNetworkDiscovery), childBytes, nil,
		); createErr != nil {
			return fmt.Errorf("schedule child chunk %d/%d: %w", i+1, len(chunks), createErr)
		}
	}

	// Record the chunk total in the plan output so GetScanProgress has a
	// denominator even before any child has run.
	jctx.Job.Output = map[string]any{"totalChunks": len(chunks)}

	log.InfoContext(ctx, "Network discovery fan-out scheduled",
		"chunk_count", len(chunks),
		"org_uid", orgUID,
		"plan_job_uid", planUID,
	)

	return nil
}
