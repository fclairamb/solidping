package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
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

// Run queries the paired Freebox channel's LAN browser, then actively probes
// each reported host through the same scanner engine the CIDR scan uses, so
// Freebox-discovered hosts get identical-quality icmpReachable / open_ports /
// suggested_checks. The Freebox-provided device name is preserved over reverse
// DNS, and any host the active scan finds unresponsive still falls back to the
// router's reachability flag so no known device is dropped.
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

	lanHosts, err := freebox.ListLanHostsForChannel(ctx, jctx.DBService, creds, orgUID, r.config.ChannelUID)
	if err != nil {
		return fmt.Errorf("list freebox lan hosts: %w", err)
	}

	// Actively probe the reported hosts, preserving each device's name as a
	// reverse-DNS override. ScanHosts inherits the default port list, timeout
	// (1s) and concurrency (64).
	inputs := make([]disc.HostInput, 0, len(lanHosts))
	for i := range lanHosts {
		ip := net.ParseIP(lanHosts[i].IP)
		if ip == nil {
			continue
		}

		inputs = append(inputs, disc.HostInput{IP: ip, HostnameHint: lanHosts[i].Name})
	}

	scanned, err := disc.ScanHosts(ctx, inputs, &disc.Config{})
	if err != nil {
		return fmt.Errorf("scan freebox hosts: %w", err)
	}

	log.InfoContext(ctx, "Freebox LAN discovery complete",
		"host_count", len(lanHosts),
		"responsive_count", len(scanned),
		"org_uid", orgUID,
		"job_uid", jobUID,
	)

	return r.persistHosts(ctx, jctx.DB, orgUID, jobUID, lanHosts, scanned, log)
}

// persistHosts upserts the Freebox-discovered hosts into discovered_hosts,
// merging the active-scan results into each Freebox host. A host the scanner
// found responsive gets real open_ports / suggested_checks and the scan's
// (hint-preserved) hostname and ICMP reachability; a host the scan did not see
// falls back to the Freebox name + reachability flag with empty ports/checks so
// it is never dropped. Mirrors NetworkDiscoveryJobRun.persistHosts: per-host
// failures are logged and skipped rather than aborting the whole run.
func (r *FreeboxLanDiscoveryJobRun) persistHosts(
	ctx context.Context,
	db *bun.DB,
	orgUID, jobUID string,
	lanHosts []freebox.LanHost,
	scanned []disc.DiscoveredHost,
	log *slog.Logger,
) error {
	byIP := make(map[string]*disc.DiscoveredHost, len(scanned))
	for i := range scanned {
		byIP[scanned[i].IP] = &scanned[i]
	}

	for idx := range lanHosts {
		lan := &lanHosts[idx]

		host, err := buildFreeboxHost(orgUID, jobUID, lan, byIP[lan.IP])
		if err != nil {
			log.WarnContext(ctx, "failed to build freebox discovered host",
				"ip", lan.IP, "error", err)

			continue
		}

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
			log.WarnContext(ctx, "failed to upsert freebox discovered host",
				"ip", lan.IP, "error", err)
			// Continue with other hosts; don't abort the whole run.
		}
	}

	return nil
}

// buildFreeboxHost assembles a DiscoveredHost row for one Freebox device,
// merging the active-scan result when present. When scan is nil (host did not
// respond to the active probe), it falls back to the Freebox-reported name and
// reachability with empty ports/checks.
func buildFreeboxHost(
	orgUID, jobUID string,
	lan *freebox.LanHost,
	scan *disc.DiscoveredHost,
) (*models.DiscoveredHost, error) {
	host := models.NewDiscoveredHost(orgUID, jobUID, lan.IP, models.DiscoverySourceFreebox)
	host.Hostname = lan.Name
	host.ICMPReachable = lan.Reachable

	if scan == nil {
		// open_ports / suggested_checks keep the "[]" defaults set by
		// NewDiscoveredHost.
		return host, nil
	}

	// HostnameHint preservation means scan.Hostname is already the Freebox name
	// when one was provided; fall back to it only if it is non-empty.
	if scan.Hostname != "" {
		host.Hostname = scan.Hostname
	}

	host.ICMPReachable = scan.ICMPReachable

	openPortsJSON, err := json.Marshal(scan.OpenPorts)
	if err != nil {
		return nil, fmt.Errorf("marshal open_ports for %s: %w", lan.IP, err)
	}

	suggestedJSON, err := json.Marshal(scan.SuggestedChecks)
	if err != nil {
		return nil, fmt.Errorf("marshal suggested_checks for %s: %w", lan.IP, err)
	}

	host.OpenPorts = openPortsJSON
	host.SuggestedChecks = suggestedJSON

	return host, nil
}
