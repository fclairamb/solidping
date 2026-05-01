package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/results"
)

func listResultsDef() ToolDefinition {
	return ToolDefinition{
		Name:        "list_results",
		Description: "Query monitoring results with flexible filtering.",
		InputSchema: objectSchema(map[string]any{
			"checkUid":         stringProp("Comma-separated check UIDs or slugs"),
			"checkType":        stringProp("Comma-separated types: http, dns, icmp, etc."),
			"status":           stringProp("Comma-separated: up, down, unknown"),
			"region":           stringProp("Comma-separated region slugs"),
			"periodType":       stringProp("Comma-separated: raw, hour, day, month"),
			"periodStartAfter": stringProp("RFC3339 timestamp (inclusive lower bound)"),
			"periodEndBefore":  stringProp("RFC3339 timestamp (exclusive upper bound)"),
			propWith: stringProp(
				"Extra fields: durationMs, durationMinMs, durationMaxMs, " +
					"region, metrics, output, availabilityPct, checkSlug, checkName",
			),
			"size":     intProp("Max results (1-100, default 20)"),
			propCursor: stringProp("Pagination cursor"),
		}, nil),
	}
}

func (h *Handler) toolListResults(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	opts := &results.ListResultsOptions{
		Cursor: getStringArg(args, "cursor"),
		Size:   getIntArg(args, "size", 20),
	}

	if opts.Size < 1 {
		opts.Size = 1
	}
	if opts.Size > 100 {
		opts.Size = 100
	}

	if v := getStringArg(args, "checkUid"); v != "" {
		opts.Checks = strings.Split(v, ",")
	}
	if v := getStringArg(args, "checkType"); v != "" {
		opts.CheckTypes = strings.Split(v, ",")
	}
	if v := getStringArg(args, "status"); v != "" {
		opts.Statuses = strings.Split(v, ",")
	}
	if v := getStringArg(args, "region"); v != "" {
		opts.Regions = strings.Split(v, ",")
	}
	if v := getStringArg(args, "periodType"); v != "" {
		opts.PeriodTypes = strings.Split(v, ",")
	}
	if v := getStringArg(args, "periodStartAfter"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.PeriodStartAfter = &t
		}
	}
	if v := getStringArg(args, "periodEndBefore"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.PeriodEndBefore = &t
		}
	}
	if v := getStringArg(args, "with"); v != "" {
		opts.With = strings.Split(v, ",")
	}

	result, err := h.resultsSvc.ListResults(ctx, orgSlug, opts)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}
