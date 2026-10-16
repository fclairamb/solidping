package scantypes

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

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
// network_discovery_plan job to enqueue.
func (d *LANDefinition) BuildJob(
	_ context.Context, deps Deps, _ string, parameters json.RawMessage,
) (string, json.RawMessage, error) {
	if err := d.Availability(deps); err != nil {
		return "", nil, err
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

	// The plan job carries no parentJobUid — children inherit it from the plan UID.
	cfg.ParentJobUID = ""

	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		return "", nil, NewError(CodeInvalidParameters, "failed to encode LAN scan config: "+err.Error())
	}

	return string(jobdef.JobTypeNetworkDiscoveryPlan), cfgBytes, nil
}
