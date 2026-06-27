package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// errNoOrganizationUID is returned when a job requires an org UID but none is set.
var errNoOrganizationUID = errors.New("network discovery job requires an organization UID")

// NetworkDiscoveryJobDefinition is the factory for network discovery jobs.
type NetworkDiscoveryJobDefinition struct{}

// Type returns the job type for network discovery jobs.
func (d *NetworkDiscoveryJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeNetworkDiscovery
}

// CreateJobRun creates a new network discovery job run from the given configuration.
func (d *NetworkDiscoveryJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg disc.Config
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("invalid network discovery config: %w", err)
	}

	if err := disc.ValidateCIDRs(cfg.CIDRs); err != nil {
		return nil, err
	}

	return &NetworkDiscoveryJobRun{config: cfg}, nil
}

// NetworkDiscoveryJobRun is an executable network discovery job instance.
type NetworkDiscoveryJobRun struct {
	config disc.Config
}

// Run executes the network discovery scan and persists the suggested checks it
// produces (grouped by host IP) into discovered_checks.
func (r *NetworkDiscoveryJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.OrganizationUID == nil {
		return errNoOrganizationUID
	}

	orgUID := *jctx.OrganizationUID

	// Suggested checks roll up under the parent plan job when this is a fan-out
	// child, so the scan-detail page sees every chunk's checks under one UID. A
	// standalone scan (no parent) persists under its own job UID.
	jobUID := jctx.Job.UID
	if r.config.ParentJobUID != "" {
		jobUID = r.config.ParentJobUID
	}

	log.InfoContext(ctx, "Starting network discovery scan",
		"cidrs", r.config.CIDRs,
		"ports", r.config.Ports,
		"org_uid", orgUID,
		"job_uid", jobUID,
		"child_job_uid", jctx.Job.UID,
		"parent_job_uid", r.config.ParentJobUID,
	)

	hosts, err := disc.Scan(ctx, &r.config)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	// Flatten every host's grouped suggested checks into a single row set.
	rows := make([]disc.SuggestedCheck, 0, len(hosts))
	for i := range hosts {
		rows = append(rows, hosts[i].SuggestedChecks...)
	}

	log.InfoContext(ctx, "Network discovery scan complete",
		"host_count", len(hosts),
		"check_count", len(rows),
		"org_uid", orgUID,
		"job_uid", jobUID,
	)

	return disc.UpsertDiscoveredChecks(ctx, jctx.DB, orgUID, jobUID, models.DiscoverySourceLAN, rows, log)
}
