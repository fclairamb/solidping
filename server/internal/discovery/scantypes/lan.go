package scantypes

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
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

// BuildJob validates {cidrs, ports?, concurrency?, timeout?} and returns the
// network_discovery_plan job to enqueue.
func (d *LANDefinition) BuildJob(
	_ context.Context, _ Deps, _ string, parameters json.RawMessage,
) (string, json.RawMessage, error) {
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
