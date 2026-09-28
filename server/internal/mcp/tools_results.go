package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/results"
)

const (
	periodTypeRaw   = "raw"
	periodTypeHour  = "hour"
	periodTypeDay   = "day"
	periodTypeMonth = "month"

	defaultListResultsSize = 20
	maxListResultsSize     = 100

	propSize       = "size"
	propPeriodType = "periodType"

	// Default lookback windows per period type. Constant names avoid
	// time-unit suffixes to keep the revive `time-naming` linter happy.
	rawDefaultWindow     = time.Hour
	hourlyDefaultWindow  = 24 * time.Hour
	dailyDefaultWindow   = 30 * 24 * time.Hour
	monthlyDefaultWindow = 365 * 24 * time.Hour
)

func listResultsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_results",
		Description: "Query monitoring results (raw or aggregated) with filtering by check, " +
			"type, status, region, period type, and time range. Use this for trend " +
			"analysis or to inspect a specific window. For investigating a single " +
			"check's current state, use diagnose_check instead.\n" +
			"Returns {data, pagination, window, effectiveFilter}: data holds the " +
			"result rows, window the time bounds actually queried, and " +
			"effectiveFilter the period filter that ran after defaulting. Defaults: " +
			"if periodType is omitted it falls back to \"hour\"; if " +
			"periodStartAfter is omitted it falls back to a window matched to the " +
			"finest periodType requested (raw=1h, hour=24h, day=30d, month=365d). " +
			"Read-only: works with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			propCheckUID: stringProp(
				"Comma-separated check UIDs or slugs to filter by, e.g. \"api-prod,db-prod\".",
			),
			"checkType": stringProp(
				"Comma-separated check types. Allowed: " + allowedCheckTypes() + ". " +
					"Example: \"http,dns\".",
			),
			propStatus: stringProp(
				"Comma-separated result statuses. Allowed: up, down, created, running, " +
					"abandoned. \"down\" covers every genuine failure (down, timeout, " +
					"error); \"abandoned\" is separate on purpose — it marks an attempt " +
					"nothing was ever reported for, which is excluded from availability " +
					"and is NOT downtime. Example: \"down\" or \"down,abandoned\".",
			),
			schemaKeyRegion: stringProp(descRegionsFilter),
			propPeriodType: stringProp(
				"Comma-separated period types. Allowed: raw (single executions), " +
					"hour, day, month (rollups). Defaults to \"hour\" when omitted. " +
					"Example: \"raw\".",
			),
			propPeriodStartAfter: stringProp(
				"RFC3339 timestamp (inclusive lower bound), e.g. \"2026-05-03T10:14:22Z\". " +
					"Defaults to a window matched to periodType when omitted (raw=1h, " +
					"hour=24h, day=30d, month=365d).",
			),
			propPeriodEndBefore: stringProp(descRFC3339Upper),
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
			propSize:   intProp(descLimit),
			propCursor: stringProp(descCursor),
		}, nil),
		OutputSchema: listResultsOutputSchema(),
		Annotations:  readOnlyAnnotations("List results"),
	}
}

// listResultsOutputSchema is list_results' output schema: the
// {data, pagination, window, effectiveFilter} envelope the handler marshals.
func listResultsOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		schemaKeyData: arrayOfObjectsProp(
			"Result rows on this page: raw executions or aggregated rollups.",
			resultResponseOutputProps(),
		),
		schemaKeyPagination: map[string]any{
			schemaKeyType:        schemaTypeObject,
			schemaKeyDescription: "Cursor pagination for this filter.",
			schemaKeyProperties: map[string]any{
				propCursor: stringProp("Opaque cursor for the next page; absent on the last page."),
				"size":     intProp("Page size used for this response."),
			},
		},
		schemaKeyWindow: map[string]any{
			schemaKeyType:        schemaTypeObject,
			schemaKeyDescription: "Time window the server actually queried.",
			schemaKeyProperties: map[string]any{
				propPeriodStartAfter: stringProp(
					"Effective inclusive lower bound (RFC3339); absent when the query has none.",
				),
				propPeriodEndBefore: stringProp(
					"Effective exclusive upper bound (RFC3339); absent when the query has none.",
				),
				"clamped": boolProp(
					"True when the server moved periodStartAfter forward (raw requests are " +
						"clamped to the raw-retention band).",
				),
			},
		},
		schemaKeyEffectiveFilter: map[string]any{
			schemaKeyType:        schemaTypeObject,
			schemaKeyDescription: "Period filter that actually ran, after the tool's defaulting.",
			schemaKeyProperties: map[string]any{
				propPeriodType:       arrayOfStringsProp("Period types queried, e.g. [\"hour\"]."),
				propPeriodStartAfter: stringProp("Applied lower bound (RFC3339); set by the default when not requested."),
				propPeriodEndBefore:  stringProp("Applied upper bound (RFC3339); absent when not requested."),
			},
		},
	}, nil)
}

// resultResponseOutputProps documents the result fields agents rely on: the
// items of list_results' data array and diagnose_check's recentResults. The
// DTO carries more (aggregated duration stats, totals, metrics, output…);
// extra properties stay allowed by default, so this documents without
// freezing the whole DTO into the contract.
func resultResponseOutputProps() map[string]any {
	return map[string]any{
		propUID:       stringProp("Result UID (time-ordered)."),
		propCheckUID:  stringProp("UID of the check this result belongs to."),
		"periodType":  stringProp("Aggregation level: raw, hour, day or month."),
		"periodStart": stringProp("RFC3339 start of the period (raw: execution time)."),
		propStatus:    stringProp("Result status: up, down, created, running, abandoned…"),
		"durationMs": map[string]any{
			schemaKeyType:        schemaTypeNumber,
			schemaKeyDescription: "Response time in ms (with=durationMs).",
		},
		schemaKeyRegion: stringProp("Region the check ran in (with=region)."),
		"availabilityPct": map[string]any{
			schemaKeyType:        schemaTypeNumber,
			schemaKeyDescription: "Uptime % for the bucket (with=availabilityPct, aggregated rows only).",
		},
		"checkSlug": stringProp("Check slug (with=checkSlug)."),
		"checkName": stringProp("Check name (with=checkName)."),
	}
}

// ListResultsResponseWithFilter wraps the upstream response with the
// effective filter that ran (post-defaulting) so the LLM can see exactly
// what it queried — silent defaulting is otherwise a footgun.
type ListResultsResponseWithFilter struct {
	*results.ListResultsResponse
	EffectiveFilter EffectiveResultsFilter `json:"effectiveFilter"`
}

// EffectiveResultsFilter records the period filter actually applied.
type EffectiveResultsFilter struct {
	PeriodType       []string   `json:"periodType"`
	PeriodStartAfter *time.Time `json:"periodStartAfter,omitempty"`
	PeriodEndBefore  *time.Time `json:"periodEndBefore,omitempty"`
}

func (h *Handler) toolListResults(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	opts := buildListResultsOptions(args, time.Now())

	resp, err := h.resultsSvc.ListResults(ctx, orgSlug, opts)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(ListResultsResponseWithFilter{
		ListResultsResponse: resp,
		EffectiveFilter: EffectiveResultsFilter{
			PeriodType:       opts.PeriodTypes,
			PeriodStartAfter: opts.PeriodStartAfter,
			PeriodEndBefore:  opts.PeriodEndBefore,
		},
	})
}

// buildListResultsOptions parses MCP args into ListResultsOptions and
// applies the periodType / periodStartAfter defaults. `now` is injected so
// tests can pin time.
func buildListResultsOptions(args map[string]any, now time.Time) *results.ListResultsOptions {
	opts := &results.ListResultsOptions{
		Cursor: getStringArg(args, propCursor),
		Size:   getIntArg(args, propSize, defaultListResultsSize),
	}
	if opts.Size < 1 {
		opts.Size = 1
	}
	if opts.Size > maxListResultsSize {
		opts.Size = maxListResultsSize
	}

	if v := getStringArg(args, propCheckUID); v != "" {
		opts.Checks = strings.Split(v, ",")
	}
	if v := getStringArg(args, "checkType"); v != "" {
		opts.CheckTypes = strings.Split(v, ",")
	}
	if v := getStringArg(args, propStatus); v != "" {
		opts.Statuses = strings.Split(v, ",")
	}
	if v := getStringArg(args, schemaKeyRegion); v != "" {
		opts.Regions = strings.Split(v, ",")
	}

	// PeriodType default
	if v := getStringArg(args, propPeriodType); v != "" {
		opts.PeriodTypes = strings.Split(v, ",")
	} else {
		opts.PeriodTypes = []string{periodTypeHour}
	}

	// Time-range defaults
	if v := getStringArg(args, propPeriodStartAfter); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.PeriodStartAfter = &t
		}
	}
	if opts.PeriodStartAfter == nil {
		cutoff := now.Add(-defaultWindowFor(finestPeriodType(opts.PeriodTypes)))
		opts.PeriodStartAfter = &cutoff
	}
	if v := getStringArg(args, propPeriodEndBefore); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.PeriodEndBefore = &t
		}
	}

	if v := getStringArg(args, "with"); v != "" {
		opts.With = strings.Split(v, ",")
	}
	return opts
}

// finestPeriodType returns the most-granular periodType in the slice. Order
// (finest to coarsest): raw, hour, day, month. Unknown values are ignored;
// an empty or all-unknown input falls back to "hour".
func finestPeriodType(periodTypes []string) string {
	rank := map[string]int{
		periodTypeRaw:   0,
		periodTypeHour:  1,
		periodTypeDay:   2,
		periodTypeMonth: 3,
	}
	const sentinel = 99
	finest := periodTypeHour
	finestRank := sentinel
	for _, p := range periodTypes {
		if r, ok := rank[p]; ok && r < finestRank {
			finest = p
			finestRank = r
		}
	}
	return finest
}

// defaultWindowFor returns the default lookback window for a periodType.
func defaultWindowFor(periodType string) time.Duration {
	switch periodType {
	case periodTypeRaw:
		return rawDefaultWindow
	case periodTypeHour:
		return hourlyDefaultWindow
	case periodTypeDay:
		return dailyDefaultWindow
	case periodTypeMonth:
		return monthlyDefaultWindow
	default:
		return hourlyDefaultWindow
	}
}
