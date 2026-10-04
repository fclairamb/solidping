package mcp

import (
	"context"
	"errors"

	"github.com/fclairamb/solidping/server/internal/handlers/checkrunnow"
)

func runCheckDef() ToolDefinition {
	return ToolDefinition{
		Name: toolRunCheck,
		Description: "Run a check once, now, in every region, instead of waiting for its next " +
			"period (use it right after a fix, or on a freshly created check). The run goes " +
			"through the normal result path: it can open or resolve an incident like any " +
			"scheduled run. Rate limited per check and per organization. Returns " +
			"{requestedAt, regions: [{region, status}]} where status is queued (due now) or " +
			"running (its current run answers); read the outcome with diagnose_check or " +
			"list_results (a result with periodStart >= requestedAt). Requires the mcp scope " +
			"(mcp:read tokens are refused) and at least the user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propIdentifier: stringProp(descIdentifier),
		}, []string{propIdentifier}),
		OutputSchema: objectSchema(map[string]any{
			"requestedAt": stringProp("RFC3339 time the request was recorded."),
			"regions": arrayOfObjectsProp("One entry per region.", map[string]any{
				"region": stringProp("Region code."),
				"status": stringProp("queued or running."),
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
