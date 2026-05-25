package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/uptrace/bun"

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

// Run executes the network discovery scan.
func (r *NetworkDiscoveryJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.OrganizationUID == nil {
		return errNoOrganizationUID
	}

	orgUID := *jctx.OrganizationUID

	// Hosts roll up under the parent plan job when this is a fan-out child, so the
	// scan-detail page sees every chunk's hosts under one UID. A standalone scan
	// (no parent) persists under its own job UID.
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

	hosts, err := disc.Scan(ctx, r.config)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	log.InfoContext(ctx, "Network discovery scan complete",
		"host_count", len(hosts),
		"org_uid", orgUID,
		"job_uid", jobUID,
	)

	if err := r.persistHosts(ctx, jctx.DB, orgUID, jobUID, hosts, log); err != nil {
		return err
	}

	return nil
}

// persistHosts upserts the discovered hosts into the database.
func (r *NetworkDiscoveryJobRun) persistHosts(
	ctx context.Context,
	db *bun.DB,
	orgUID, jobUID string,
	hosts []disc.DiscoveredHost,
	log *slog.Logger,
) error {
	for idx := range hosts {
		discovered := &hosts[idx]
		openPortsJSON, err := json.Marshal(discovered.OpenPorts)
		if err != nil {
			return fmt.Errorf("marshal open_ports for %s: %w", discovered.IP, err)
		}

		suggestedJSON, err := json.Marshal(discovered.SuggestedChecks)
		if err != nil {
			return fmt.Errorf("marshal suggested_checks for %s: %w", discovered.IP, err)
		}

		host := models.NewDiscoveredHost(orgUID, jobUID, discovered.IP, models.DiscoverySourceLAN)
		host.Hostname = discovered.Hostname
		host.ICMPReachable = discovered.ICMPReachable
		host.OpenPorts = openPortsJSON
		host.SuggestedChecks = suggestedJSON

		_, err = db.NewInsert().
			Model(host).
			On("CONFLICT (organization_uid, ip, source) WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL DO UPDATE").
			Set("job_uid = EXCLUDED.job_uid").
			Set("hostname = EXCLUDED.hostname").
			Set("open_ports = EXCLUDED.open_ports").
			Set("icmp_reachable = EXCLUDED.icmp_reachable").
			Set("suggested_checks = EXCLUDED.suggested_checks").
			Set("discovered_at = EXCLUDED.discovered_at").
			Exec(ctx)
		if err != nil {
			log.WarnContext(ctx, "failed to upsert discovered host",
				"ip", discovered.IP, "error", err)
			// Continue with other hosts; don't abort the whole scan.
		}
	}

	return nil
}
