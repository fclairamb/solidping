package scantypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/discoverytask"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// maxPort is the highest TCP port.
const maxPort = 65535

// LANDefinition is the CIDR network-scan discovery type. It validates the
// requested CIDRs against the fan-out ceiling and enqueues the plan job, which
// owns the chunking (unchanged by this spec).
type LANDefinition struct{}

// Type returns the user-facing discovery type id.
func (d *LANDefinition) Type() string { return "lan" }

// Source returns the row source written by LAN scans.
func (d *LANDefinition) Source() models.DiscoverySource { return models.DiscoverySourceLAN }

// ErrLANNotAvailableMessage is the refusal message of a `lan` scan under an
// enforcing egress policy. It names the knob an operator can flip.
const ErrLANNotAvailableMessage = "LAN discovery is not available: the egress policy of this deployment " +
	"refuses private targets; an operator can allow them with " + egress.EnvAllowPrivate + "=true"

// LANNotAvailable returns the coded refusal of a `lan` scan when guard is
// enforcing, nil otherwise. The plan job re-checks it so a plan queued before
// the policy applied does not fan out.
func LANNotAvailable(guard *egress.Guard) error {
	if guard.Enforcing() {
		return NewError(CodeNotAvailable, ErrLANNotAvailableMessage)
	}

	return nil
}

// Availability refuses `lan` whenever the process egress guard is enforcing:
// a scan of public ranges only would turn the platform into an internet port
// scanner, and private ranges are exactly what the policy forbids.
func (d *LANDefinition) Availability(deps Deps) error {
	return LANNotAvailable(deps.EgressGuard)
}

// BuildJob validates {cidrs, ports?, concurrency?, timeout?} and returns the
// network_discovery_plan job to enqueue, or, with a region, the
// network_discovery_agent_plan job that the region's agents run.
func (d *LANDefinition) BuildJob(
	ctx context.Context, deps Deps, orgUID, region string, parameters json.RawMessage,
) (string, json.RawMessage, error) {
	// A region-bound scan never runs from the server: the server's egress
	// policy does not apply to it.
	if region == "" {
		if err := d.Availability(deps); err != nil {
			return "", nil, err
		}
	}

	var cfg disc.Config

	if len(parameters) > 0 {
		if err := json.Unmarshal(parameters, &cfg); err != nil {
			return "", nil, NewError(CodeInvalidParameters, "invalid LAN scan parameters: "+err.Error())
		}
	}

	if len(cfg.CIDRs) == 0 {
		return "", nil, NewError(CodeInvalidParameters, "cidrs is required")
	}

	if err := disc.ValidatePlanCIDRs(cfg.CIDRs); err != nil {
		// Range-too-large is a parameter problem; surface it as invalid parameters
		// with the underlying message (preserves the size detail).
		if errors.Is(err, disc.ErrRangeTooLarge) {
			return "", nil, NewError(disc.ErrCodeRangeTooLarge, err.Error())
		}

		return "", nil, NewError(CodeInvalidParameters, err.Error())
	}

	if region != "" {
		return buildAgentPlan(ctx, deps, orgUID, region, &cfg)
	}

	// The plan job carries no parentJobUid — children inherit it from the plan UID.
	cfg.ParentJobUID = ""

	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		return "", nil, NewError(CodeInvalidParameters, "failed to encode LAN scan config: "+err.Error())
	}

	return string(jobdef.JobTypeNetworkDiscoveryPlan), cfgBytes, nil
}

// buildAgentPlan checks a region-bound scan: the canonical `@slug` of one of
// the org's private regions, served right now by a live agent reporting
// discovery, and at most discoverytask.MaxPorts ports. Concurrency and timeout
// are the agent's own.
func buildAgentPlan(
	ctx context.Context, deps Deps, orgUID, region string, cfg *disc.Config,
) (string, json.RawMessage, error) {
	if _, ok := regions.ParsePrivateRegion(region); !ok || deps.Regions == nil {
		return "", nil, NewError(CodeInvalidParameters, "region must be one of the organization's private regions (@slug)")
	}

	owned, err := deps.Regions.OwnsPrivateRegion(ctx, orgUID, region)
	if err != nil {
		return "", nil, fmt.Errorf("check discovery region: %w", err)
	}

	if !owned {
		return "", nil, NewError(CodeInvalidParameters, "region must be one of the organization's private regions (@slug)")
	}

	ports, err := agentPorts(cfg.Ports)
	if err != nil {
		return "", nil, err
	}

	live, err := deps.Regions.HasLiveAgentWith(ctx, orgUID, region, discoverytask.Capability)
	if err != nil {
		return "", nil, fmt.Errorf("check discovery agents: %w", err)
	}

	if !live {
		return "", nil, NewError(CodeRegionUnavailable,
			"no live agent of "+region+" runs discovery: its operator must set SP_AGENT_DISCOVERY_ENABLED")
	}

	cfgBytes, err := json.Marshal(discoverytask.PlanConfig{CIDRs: cfg.CIDRs, Ports: ports, Region: region})
	if err != nil {
		return "", nil, fmt.Errorf("encode agent LAN scan config: %w", err)
	}

	return string(jobdef.JobTypeNetworkDiscoveryAgentPlan), cfgBytes, nil
}

// agentPorts resolves the ports of a region-bound scan: the default list when
// none, else 1..65535, deduplicated, at most discoverytask.MaxPorts.
func agentPorts(requested []int) ([]int, error) {
	if len(requested) == 0 {
		return disc.DefaultPortList(), nil
	}

	ports := make([]int, 0, len(requested))

	for _, port := range requested {
		if port < 1 || port > maxPort {
			return nil, NewError(CodeInvalidParameters, fmt.Sprintf("invalid port %d", port))
		}

		if !slices.Contains(ports, port) {
			ports = append(ports, port)
		}
	}

	if len(ports) > discoverytask.MaxPorts {
		return nil, NewError(CodeInvalidParameters,
			fmt.Sprintf("at most %d ports per agent scan, got %d", discoverytask.MaxPorts, len(ports)))
	}

	return ports, nil
}
