package discovery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// defaultContainerListTimeout is the per-endpoint timeout applied when the job
// config does not set one.
const defaultContainerListTimeout = 10 * time.Second

// DiscoveredContainer is one running container enumerated from a configured
// Docker-compatible endpoint. It is the engine's output, independent of the job
// plumbing and of the suggested-check shape, so it can be unit-tested against a
// fake Docker API.
type DiscoveredContainer struct {
	ID           string          // full container ID — the group identity
	Name         string          // container name, leading slash stripped
	Image        string          // image reference
	State        string          // e.g. "running"
	Status       string          // human status, e.g. "Up 2 hours (healthy)"
	HealthStatus string          // parsed from Status when present, else ""
	Ports        []ContainerPort // published + exposed ports
}

// ListContainers connects to one Docker-compatible endpoint and returns its
// running containers. It builds the client exactly as the docker checker does
// (host + API-version negotiation), so it works against Docker and Podman
// sockets and tcp:// endpoints unchanged. The caller is responsible for
// per-endpoint error handling (log-and-continue).
func ListContainers(ctx context.Context, endpoint string, timeout time.Duration) ([]DiscoveredContainer, error) {
	if timeout <= 0 {
		timeout = defaultContainerListTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cli, err := client.NewClientWithOpts(
		client.WithHost(endpoint),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("create docker client for %q: %w", endpoint, err)
	}

	defer func() { _ = cli.Close() }()

	// All:false → running containers only; the suggestion set stays scoped to
	// things currently worth monitoring (decision 3 in the spec).
	summaries, err := cli.ContainerList(ctx, container.ListOptions{All: false})
	if err != nil {
		return nil, fmt.Errorf("list containers on %q: %w", endpoint, err)
	}

	out := make([]DiscoveredContainer, 0, len(summaries))
	for i := range summaries {
		out = append(out, mapSummary(summaries[i]))
	}

	return out, nil
}

// mapSummary converts a Docker container summary into a DiscoveredContainer.
func mapSummary(s container.Summary) DiscoveredContainer {
	ports := make([]ContainerPort, 0, len(s.Ports))
	for _, p := range s.Ports {
		ports = append(ports, ContainerPort{
			PrivatePort: p.PrivatePort,
			PublicPort:  p.PublicPort,
			Type:        p.Type,
		})
	}

	return DiscoveredContainer{
		ID:           s.ID,
		Name:         containerName(s.Names),
		Image:        s.Image,
		State:        s.State,
		Status:       s.Status,
		HealthStatus: parseHealthStatus(s.Status),
		Ports:        ports,
	}
}

// containerName returns the first name with its leading slash stripped (Docker
// reports names as "/web"), mirroring how the docker checker derives its name.
func containerName(names []string) string {
	if len(names) == 0 {
		return ""
	}

	return strings.TrimPrefix(names[0], "/")
}

// parseHealthStatus extracts the health word from a container status string. The
// list endpoint does not return State.Health, but the Status string embeds it as
// a parenthesised suffix when a HEALTHCHECK is declared, e.g. "Up 2 hours
// (healthy)" / "Up 5 minutes (unhealthy)" / "Up 1 second (health: starting)".
func parseHealthStatus(status string) string {
	open := strings.LastIndex(status, "(")
	if open < 0 {
		return ""
	}

	end := strings.LastIndex(status, ")")
	if end <= open {
		return ""
	}

	inner := strings.TrimSpace(status[open+1 : end])

	// "health: starting" → "starting"; "healthy"/"unhealthy" pass through.
	if rest, ok := strings.CutPrefix(inner, "health:"); ok {
		inner = strings.TrimSpace(rest)
	}

	switch inner {
	case "healthy", "unhealthy", "starting":
		return inner
	default:
		return ""
	}
}
