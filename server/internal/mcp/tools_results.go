package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/results"
)

func listResultsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_results",
		Description: "Query monitoring results (raw or aggregated) with filtering by check, " +
			"type, status, region, period type, and time range. Use this for trend " +
			"analysis or to inspect a specific window. For investigating a single " +
			"check's current state, use diagnose_check instead.",
		InputSchema: objectSchema(map[string]any{
			"checkUid": stringProp(
				"Comma-separated check UIDs or slugs to filter by, e.g. \"api-prod,db-prod\".",
			),
			"checkType": stringProp(
				"Comma-separated check types. Allowed: http, tcp, icmp, dns, ssl, heartbeat, domain. " +
					"Example: \"http,dns\".",
			),
			"status": stringProp(
				"Comma-separated check statuses. Allowed: up, down, unknown. " +
					"Example: \"down\" or \"down,unknown\".",
			),
			"region": stringProp(descRegionsFilter),
			"periodType": stringProp(
				"Comma-separated period types. Allowed: raw (single executions), " +
					"hour, day, month (rollups). Example: \"raw\".",
			),
			"periodStartAfter": stringProp(descRFC3339Lower),
			"periodEndBefore":  stringProp(descRFC3339Upper),
			propWith: stringProp(
				"Comma-separated extra fields:\n" +
					"  durationMs       — response time in ms\n" +
					"  durationMinMs    — min response time in the bucket (aggregated rows only)\n" +
					"  durationMaxMs    — max response time in the bucket (aggregated rows only)\n" +
					"  region           — region the check ran in\n" +
					"  metrics          — per-execution metrics\n" +
					"  output           — full check output incl. error messages\n" +
					"  availabilityPct  — uptime % (aggregated rows only)\n" +
					"  checkSlug        — slug of the check\n" +
					"  checkName        — human name of the check\n" +
					"Example: \"durationMs,output\".",
			),
			"size":     intProp(descLimit),
			propCursor: stringProp(descCursor),
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
