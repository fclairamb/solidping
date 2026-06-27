package scantypes

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// containerParams is the parameter schema for a container discovery scan. It is
// also the job config payload (the container_discovery job reads the same
// {hosts, timeout} shape), so it is reused on both sides.
type containerParams struct {
	Hosts   []string `json:"hosts"`
	Timeout string   `json:"timeout,omitempty"`
}

// ContainerDefinition is the configured-endpoint container discovery type. It
// validates that every endpoint is a supported Docker-compatible scheme before
// enqueuing the job. v1 supports unix:// and plaintext tcp:// only — matching
// what the docker checker itself supports (no TLS client certs).
type ContainerDefinition struct{}

// Type returns the user-facing discovery type id.
func (d *ContainerDefinition) Type() string { return "container" }

// Source returns the row source written by container scans.
func (d *ContainerDefinition) Source() models.DiscoverySource {
	return models.DiscoverySourceContainer
}

// BuildJob validates {hosts, timeout?} — at least one endpoint, each one
// unix:// or tcp:// — and returns the container_discovery job to enqueue. A bad
// endpoint scheme returns the foundation's DISCOVERY_INVALID_PARAMETERS.
func (d *ContainerDefinition) BuildJob(
	_ context.Context, _ Deps, _ string, parameters json.RawMessage,
) (string, json.RawMessage, error) {
	var params containerParams

	if len(parameters) > 0 {
		if err := json.Unmarshal(parameters, &params); err != nil {
			return "", nil, NewError(CodeInvalidParameters, "invalid container scan parameters: "+err.Error())
		}
	}

	hosts := make([]string, 0, len(params.Hosts))

	for _, h := range params.Hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}

		if !isSupportedDockerEndpoint(h) {
			return "", nil, NewError(
				CodeInvalidParameters,
				"endpoint must start with unix:// or tcp:// (got "+h+")",
			)
		}

		hosts = append(hosts, h)
	}

	if len(hosts) == 0 {
		return "", nil, NewError(CodeInvalidParameters, "at least one Docker endpoint is required")
	}

	params.Hosts = hosts

	cfgBytes, err := json.Marshal(params)
	if err != nil {
		return "", nil, NewError(CodeInvalidParameters, "failed to encode container scan config: "+err.Error())
	}

	return string(jobdef.JobTypeContainerDiscovery), cfgBytes, nil
}

// isSupportedDockerEndpoint mirrors the docker checker's accepted host schemes:
// a local socket (unix://) or a plaintext remote daemon (tcp://). mTLS (2376)
// endpoints are a deferred follow-up.
func isSupportedDockerEndpoint(endpoint string) bool {
	return strings.HasPrefix(endpoint, "unix://") || strings.HasPrefix(endpoint, "tcp://")
}
