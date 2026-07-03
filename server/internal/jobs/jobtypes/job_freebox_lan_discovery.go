package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

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
// Freebox-discovered hosts get identical-quality suggested checks. The
// Freebox-provided device name is preserved over reverse DNS, and any host the
// active scan finds unresponsive still falls back to the router's reachability
// flag (an ICMP suggestion when reachable) so no known device is dropped.
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

	rows := buildFreeboxRows(lanHosts, scanned)

	log.InfoContext(ctx, "Freebox LAN discovery complete",
		"host_count", len(lanHosts),
		"responsive_count", len(scanned),
		"check_count", len(rows),
		"org_uid", orgUID,
		"job_uid", jobUID,
	)

	return disc.UpsertDiscoveredChecks(ctx, jctx.DB, orgUID, jobUID, models.DiscoverySourceFreebox, rows, log)
}

// buildFreeboxRows turns each Freebox device into suggested-check rows. A host
// the scanner found responsive contributes the scan's grouped suggested checks
// (which already preserve the Freebox name as the group label); a host the scan
// did not see falls back to the Freebox name + reachability flag — an ICMP
// suggestion when reachable, or nothing — so it is never silently dropped.
func buildFreeboxRows(lanHosts []freebox.LanHost, scanned []disc.DiscoveredHost) []disc.SuggestedCheck {
	byIP := make(map[string]*disc.DiscoveredHost, len(scanned))
	for i := range scanned {
		byIP[scanned[i].IP] = &scanned[i]
	}

	rows := make([]disc.SuggestedCheck, 0, len(lanHosts))

	for idx := range lanHosts {
		lan := &lanHosts[idx]

		if scan := byIP[lan.IP]; scan != nil {
			rows = append(rows, scan.SuggestedChecks...)

			continue
		}

		// Fallback for an unscannable host: an ICMP suggestion when the Freebox
		// reports it reachable. SuggestChecks returns no rows when not reachable.
		rows = append(rows, disc.SuggestChecks(lan.IP, lan.Name, lan.Reachable, nil)...)
	}

	return rows
}
