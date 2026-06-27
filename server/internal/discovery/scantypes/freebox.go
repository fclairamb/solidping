package scantypes

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// freeboxParams is the parameter schema for a Freebox discovery scan. It is also
// the job config payload (the freebox_lan_discovery job reads the same
// {channelUid} shape), so it is reused on both sides.
type freeboxParams struct {
	ChannelUID string `json:"channelUid"`
}

// FreeboxDefinition is the Freebox LAN-browser discovery type. It validates that
// the targeted channel exists and is paired (granted) before enqueuing the job.
type FreeboxDefinition struct{}

// Type returns the user-facing discovery type id.
func (d *FreeboxDefinition) Type() string { return "freebox" }

// Source returns the row source written by Freebox scans.
func (d *FreeboxDefinition) Source() models.DiscoverySource { return models.DiscoverySourceFreebox }

// BuildJob validates {channelUid} resolves to a granted Freebox channel and
// returns the freebox_lan_discovery job to enqueue. It probes the channel so a
// 404/409 surfaces before a job is queued that would just fail on first run.
func (d *FreeboxDefinition) BuildJob(
	ctx context.Context, deps Deps, orgUID string, parameters json.RawMessage,
) (string, json.RawMessage, error) {
	var params freeboxParams

	if len(parameters) > 0 {
		if err := json.Unmarshal(parameters, &params); err != nil {
			return "", nil, NewError(CodeInvalidParameters, "invalid Freebox scan parameters: "+err.Error())
		}
	}

	if params.ChannelUID == "" {
		return "", nil, NewError(CodeInvalidParameters, "channelUid is required")
	}

	if deps.DBService == nil {
		return "", nil, NewError(CodeInvalidParameters, "freebox scan requires database access")
	}

	// Fail fast: probe the channel via the shared resolver.
	if _, err := freebox.ListLanHostsForChannel(
		ctx, deps.DBService, deps.Creds, orgUID, params.ChannelUID,
	); err != nil {
		switch {
		case errors.Is(err, freebox.ErrFreeboxNotGranted):
			return "", nil, NewError(CodeFreeboxNotGranted, "Freebox channel is not paired yet")
		case errors.Is(err, freebox.ErrFreeboxChannelNotFound):
			return "", nil, NewError(CodeInvalidParameters, "freebox channel not found")
		default:
			return "", nil, err
		}
	}

	cfgBytes, err := json.Marshal(params)
	if err != nil {
		return "", nil, NewError(CodeInvalidParameters, "failed to encode Freebox scan config: "+err.Error())
	}

	return string(jobdef.JobTypeFreeboxLanDiscovery), cfgBytes, nil
}
