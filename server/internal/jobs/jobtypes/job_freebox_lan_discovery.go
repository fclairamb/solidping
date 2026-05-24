package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// errFreeboxNoChannel is returned when a freebox_lan_discovery job is created
// without a channelUid in its config.
var errFreeboxNoChannel = errors.New("freebox LAN discovery job requires a channelUid")

// FreeboxLanDiscoveryConfig is the job configuration for a Freebox LAN
// discovery run.
type FreeboxLanDiscoveryConfig struct {
	ChannelUID string `json:"channelUid"`
}

// FreeboxLanDiscoveryJobDefinition is the factory for Freebox LAN discovery jobs.
type FreeboxLanDiscoveryJobDefinition struct{}

// Type returns the job type for Freebox LAN discovery jobs.
func (d *FreeboxLanDiscoveryJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeFreeboxLanDiscovery
}

// CreateJobRun creates a new Freebox LAN discovery run from the given config.
func (d *FreeboxLanDiscoveryJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg FreeboxLanDiscoveryConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("invalid freebox LAN discovery config: %w", err)
	}

	if cfg.ChannelUID == "" {
		return nil, errFreeboxNoChannel
	}

	return &FreeboxLanDiscoveryJobRun{config: cfg}, nil
}

// FreeboxLanDiscoveryJobRun is an executable Freebox LAN discovery instance.
type FreeboxLanDiscoveryJobRun struct {
	config FreeboxLanDiscoveryConfig
}

// Run queries the paired Freebox channel's LAN browser and persists each host
// into discovered_hosts with source='freebox'. The single Freebox API call is
// wrapped in the async job machinery purely for consistency with
// network_discovery — it is cheap (no scan engine, no CIDR expansion).
func (r *FreeboxLanDiscoveryJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.OrganizationUID == nil {
		return errNoOrganizationUID
	}

	orgUID := *jctx.OrganizationUID
	jobUID := jctx.Job.UID

	log.InfoContext(ctx, "Starting Freebox LAN discovery",
		"channel_uid", r.config.ChannelUID,
		"org_uid", orgUID,
		"job_uid", jobUID,
	)

	creds := jctx.Services.Credentials

	hosts, err := freebox.ListLanHostsForChannel(ctx, jctx.DBService, creds, orgUID, r.config.ChannelUID)
	if err != nil {
		return fmt.Errorf("list freebox lan hosts: %w", err)
	}

	log.InfoContext(ctx, "Freebox LAN discovery complete",
		"host_count", len(hosts),
		"org_uid", orgUID,
		"job_uid", jobUID,
	)

	return r.persistHosts(ctx, jctx.DB, orgUID, jobUID, hosts, log)
}

// persistHosts upserts the Freebox-discovered hosts into discovered_hosts.
// Mirrors NetworkDiscoveryJobRun.persistHosts: per-host failures are logged
// and skipped rather than aborting the whole run.
func (r *FreeboxLanDiscoveryJobRun) persistHosts(
	ctx context.Context,
	db *bun.DB,
	orgUID, jobUID string,
	hosts []freebox.LanHost,
	log *slog.Logger,
) error {
	for idx := range hosts {
		lan := &hosts[idx]

		host := models.NewDiscoveredHost(orgUID, jobUID, lan.IP, models.DiscoverySourceFreebox)
		host.Hostname = lan.Name
		host.ICMPReachable = lan.Reachable
		// Freebox gives us name + reachability only — no port scan, no
		// suggested checks. open_ports / suggested_checks stay "[]" (the
		// promote flow falls back to config["host"] = host.IP).

		_, err := db.NewInsert().
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
			log.WarnContext(ctx, "failed to upsert freebox discovered host",
				"ip", lan.IP, "error", err)
			// Continue with other hosts; don't abort the whole run.
		}
	}

	return nil
}
