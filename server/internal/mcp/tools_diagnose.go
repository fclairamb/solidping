package mcp

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/results"
)

const (
	diagnoseDefaultRecentResults = 5
	diagnoseMaxRecentResults     = 20
	diagnoseMaxRawFetch          = 100
	propRecentResultsLimit       = "recentResultsLimit"
)

func diagnoseCheckDef() ToolDefinition {
	return ToolDefinition{
		Name: "diagnose_check",
		Description: "Return everything an operator would want to look at to diagnose " +
			"a single check's current state in one call: current status, recent raw " +
			"results across regions, any active incident, and the most recent resolved " +
			"incident. Use this instead of chaining list_results + list_incidents " +
			"when a human asks \"what's wrong with check X?\".",
		InputSchema: objectSchema(map[string]any{
			propIdentifier:         stringProp("Check UID or slug (e.g. \"api-prod\" or a UUID)."),
			propRecentResultsLimit: intProp("Recent raw results per region (1-20, default 5)."),
		}, []string{propIdentifier}),
	}
}

// DiagnoseCheckResult is the JSON shape returned by the diagnose_check tool.
type DiagnoseCheckResult struct {
	Check                checks.CheckResponse        `json:"check"`
	RecentResults        []results.ResultResponse    `json:"recentResults"`
	ActiveIncident       *incidents.IncidentResponse `json:"activeIncident"`
	LastResolvedIncident *incidents.IncidentResponse `json:"lastResolvedIncident"`
}

func (h *Handler) toolDiagnoseCheck(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	identifier := getStringArg(args, propIdentifier)
	if identifier == "" {
		return errorResult("identifier is required")
	}

	perRegion := clampPerRegion(getIntArg(args, propRecentResultsLimit, diagnoseDefaultRecentResults))

	check, err := h.checksSvc.GetCheck(ctx, orgSlug, identifier, checks.GetCheckOptions{
		IncludeLastStatusChange: true,
	})
	if err != nil {
		return errorResult(err.Error())
	}

	recent, err := h.fetchRecentResults(ctx, orgSlug, &check, perRegion)
	if err != nil {
		return errorResult(err.Error())
	}

	active := h.fetchSingleIncident(ctx, orgSlug, check.UID, "active")
	resolved := h.fetchSingleIncident(ctx, orgSlug, check.UID, "resolved")

	return marshalResult(buildDiagnoseResponse(&check, recent, active, resolved, perRegion))
}

func clampPerRegion(value int) int {
	if value < 1 {
		return 1
	}
	if value > diagnoseMaxRecentResults {
		return diagnoseMaxRecentResults
	}
	return value
}

func (h *Handler) fetchRecentResults(
	ctx context.Context, orgSlug string, check *checks.CheckResponse, perRegion int,
) ([]results.ResultResponse, error) {
	regionCount := len(check.Regions)
	if regionCount < 1 {
		regionCount = 1
	}
	rawSize := perRegion * regionCount
	if rawSize > diagnoseMaxRawFetch {
		rawSize = diagnoseMaxRawFetch
	}

	resp, err := h.resultsSvc.ListResults(ctx, orgSlug, &results.ListResultsOptions{
		Checks:      []string{check.UID},
		PeriodTypes: []string{"raw"},
		Size:        rawSize,
		With:        []string{"region", "output", "durationMs"},
	})
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (h *Handler) fetchSingleIncident(
	ctx context.Context, orgSlug, checkUID, state string,
) *incidents.IncidentResponse {
	resp, err := h.incidentsSvc.ListIncidents(ctx, orgSlug, &incidents.ListIncidentsOptions{
		CheckUIDs: []string{checkUID},
		States:    []string{state},
		Size:      1,
	})
	if err != nil || resp == nil || len(resp.Data) == 0 {
		return nil
	}
	inc := resp.Data[0]
	return &inc
}

// buildDiagnoseResponse trims the recent results to at most perRegion entries
// per region (preserving the upstream DESC-by-time ordering) and assembles the
// final result struct. It is a pure function for easy unit testing.
func buildDiagnoseResponse(
	check *checks.CheckResponse,
	recent []results.ResultResponse,
	active, resolved *incidents.IncidentResponse,
	perRegion int,
) DiagnoseCheckResult {
	return DiagnoseCheckResult{
		Check:                *check,
		RecentResults:        trimResultsPerRegion(recent, perRegion),
		ActiveIncident:       active,
		LastResolvedIncident: resolved,
	}
}

func trimResultsPerRegion(recent []results.ResultResponse, perRegion int) []results.ResultResponse {
	if perRegion < 1 || len(recent) == 0 {
		return []results.ResultResponse{}
	}
	counts := make(map[string]int, len(recent))
	out := make([]results.ResultResponse, 0, len(recent))
	for i := range recent {
		region := ""
		if recent[i].Region != nil {
			region = *recent[i].Region
		}
		if counts[region] >= perRegion {
			continue
		}
		out = append(out, recent[i])
		counts[region]++
	}
	return out
}
