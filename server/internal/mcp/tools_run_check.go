package mcp

import (
	"context"
	"errors"

	"github.com/fclairamb/solidping/server/internal/handlers/checkrunnow"
)

func runCheckDef() ToolDefinition {
	return ToolDefinition{
		Name: toolRunCheck,
		Description: "Run an existing check once, now, in every one of its regions, instead of " +
			"waiting for its next period, e.g. right after a fix or on a freshly created check. " +
			"identifier is the check UID or slug as list_checks returns; an unknown one fails " +
			"with not found and queues nothing, and a paused check (enabled:false) is refused. " +
			"The run takes the normal result path, so it " +
			"can open or resolve an incident like a scheduled run. It is rate limited per check " +
			"and per organization. A region status of queued means due now, or right after the " +
			"run in progress. Read the outcome with diagnose_check or list_results: a result " +
			"with periodStart >= requestedAt. To try a target without a saved check, use probe. " +
			"Requires the mcp scope (mcp:read tokens are refused) and at least the user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propIdentifier: stringProp(descIdentifier),
		}, []string{propIdentifier}),
		OutputSchema: objectSchema(map[string]any{
			"requestedAt": stringProp("RFC3339 time the request was recorded."),
			"regions": arrayOfObjectsProp("One entry per region.", map[string]any{
				"region":   stringProp("Region code."),
				propStatus: stringProp("queued."),
			}),
		}, []string{"requestedAt", "regions"}),
		Annotations: createAnnotations("Run check now"),
	}
}

func (h *Handler) toolRunCheck(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	identifier := getStringArg(args, propIdentifier)
	if identifier == "" {
		return errorResult("identifier is required")
	}

	resp, err := h.runNowSvc.RunNow(ctx, orgSlug, identifier)
	if err != nil {
		var limited *checkrunnow.RateLimitedError
		if errors.As(err, &limited) {
			return errorResult(limited.Error())
		}

		return errorResult(err.Error())
	}

	return marshalResult(resp)
}
