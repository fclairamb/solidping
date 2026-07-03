package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// errContainerNoHosts is returned when a container_discovery job is created
// without at least one endpoint in its config.
var errContainerNoHosts = errors.New("container discovery job requires at least one host")

// ContainerDiscoveryConfig is the job configuration for a container discovery
// run. Hosts is the explicit list of Docker-compatible endpoints to enumerate;
// Timeout is the per-endpoint timeout (default 10s).
type ContainerDiscoveryConfig struct {
	Hosts   []string `json:"hosts"`
	Timeout string   `json:"timeout,omitempty"`
}

// ContainerDiscoveryJobDefinition is the factory for container discovery jobs.
type ContainerDiscoveryJobDefinition struct{}

// Type returns the job type for container discovery jobs.
func (d *ContainerDiscoveryJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeContainerDiscovery
}

// CreateJobRun creates a new container discovery run from the given config.
func (d *ContainerDiscoveryJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg ContainerDiscoveryConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("invalid container discovery config: %w", err)
	}

	if len(cfg.Hosts) == 0 {
		return nil, errContainerNoHosts
	}

	return &ContainerDiscoveryJobRun{config: cfg}, nil
}

// ContainerDiscoveryJobRun is an executable container discovery instance.
type ContainerDiscoveryJobRun struct {
	config ContainerDiscoveryConfig
}

// Run iterates the configured Docker-compatible endpoints, lists running
// containers on each, turns every container into a group of suggested-check rows
// (a docker check plus one http/tcp check per published port), and persists them
// via the shared UpsertDiscoveredChecks (source='container'). A per-endpoint
// failure is logged and skipped — it never aborts the run, mirroring the
// log-and-continue resilience contract of the other discovery jobs.
func (r *ContainerDiscoveryJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.OrganizationUID == nil {
		return errNoOrganizationUID
	}

	orgUID := *jctx.OrganizationUID
	jobUID := jctx.Job.UID

	timeout := r.resolveTimeout()

	log.InfoContext(ctx, "Starting container discovery",
		"host_count", len(r.config.Hosts),
		"org_uid", orgUID,
		"job_uid", jobUID,
	)

	var rows []disc.SuggestedCheck

	for _, host := range r.config.Hosts {
		containers, err := disc.ListContainers(ctx, host, timeout)
		if err != nil {
			// Per-endpoint failure: log and skip, never abort the whole run.
			log.WarnContext(ctx, "failed to list containers on endpoint",
				"host", host, "error", err)

			continue
		}

		for i := range containers {
			rows = append(rows, buildContainerRows(host, &containers[i])...)
		}
	}

	log.InfoContext(ctx, "Container discovery complete",
		"host_count", len(r.config.Hosts),
		"check_count", len(rows),
		"org_uid", orgUID,
		"job_uid", jobUID,
	)

	return disc.UpsertDiscoveredChecks(ctx, jctx.DB, orgUID, jobUID, models.DiscoverySourceContainer, rows, log)
}

// resolveTimeout parses the configured per-endpoint timeout, falling back to 0
// (which the engine treats as its own default) when unset or unparseable.
func (r *ContainerDiscoveryJobRun) resolveTimeout() time.Duration {
	if r.config.Timeout == "" {
		return 0
	}

	d, err := time.ParseDuration(r.config.Timeout)
	if err != nil {
		return 0
	}

	return d
}

// buildContainerRows turns one discovered container into its group of
// suggested-check rows, carrying the endpoint as the docker host.
func buildContainerRows(host string, cont *disc.DiscoveredContainer) []disc.SuggestedCheck {
	return disc.SuggestContainerChecks(&disc.ContainerSuggestInput{
		ContainerID:  cont.ID,
		Name:         cont.Name,
		Image:        cont.Image,
		State:        cont.State,
		HealthStatus: cont.HealthStatus,
		DockerHost:   host,
		Ports:        cont.Ports,
	})
}
