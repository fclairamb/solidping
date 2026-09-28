package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
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
		Name: toolDiagnoseCheck,
		Description: "Return everything an operator would want to look at to diagnose " +
			"a single check's current state in one call: current status, recent raw " +
			"results across regions, any active incident, and the most recent resolved " +
			"incident. Use this instead of chaining list_results + list_incidents " +
			"when a human asks \"what's wrong with check X?\". Returns {check, " +
			"freshness, regionalIssue, recentResults, activeIncident, " +
			"lastResolvedIncident} — freshness and regionalIssue are omitted when " +
			"there is nothing to report, the incidents are null when there are none. " +
			"Read-only: works with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			propIdentifier:         stringProp("Check UID or slug (e.g. \"api-prod\" or a UUID)."),
			propRecentResultsLimit: intProp("Recent raw results per region (1-20, default 5)."),
		}, []string{propIdentifier}),
		OutputSchema: objectSchema(map[string]any{
			schemaKeyCheck: checkOutputSchema(),
			"freshness": arrayOfStringsProp(
				"One sentence per silent region naming it and since when it has gone " +
					"quiet; empty when every region is reporting.",
			),
			"regionalIssue": stringProp(
				"One sentence describing the regional issue (some, but fewer than the " +
					"quorum, of the check's regions failing); empty when there is none.",
			),
			"recentResults": arrayOfObjectsProp(
				"Newest-first raw results, trimmed to recentResultsLimit per region; "+
					"includes region, output and durationMs.",
				resultResponseOutputProps(),
			),
			"activeIncident": map[string]any{
				schemaKeyType:        []string{schemaTypeObject, schemaTypeNull},
				schemaKeyDescription: "Currently-open incident for the check, or null when none is open.",
				schemaKeyProperties:  diagnoseIncidentOutputProps(),
			},
			"lastResolvedIncident": map[string]any{
				schemaKeyType:        []string{schemaTypeObject, schemaTypeNull},
				schemaKeyDescription: "Most recently resolved incident for the check, or null when there is none.",
				schemaKeyProperties:  diagnoseIncidentOutputProps(),
			},
		}, []string{schemaKeyCheck, "recentResults", "activeIncident", "lastResolvedIncident"}),
		Annotations: readOnlyAnnotations("Diagnose check"),
	}
}

// diagnoseIncidentOutputProps documents the incident fields the diagnosis
// surfaces. The DTO carries more (acknowledgement, snooze, membership…);
// extra properties stay allowed by default.
func diagnoseIncidentOutputProps() map[string]any {
	return map[string]any{
		propUID:             stringProp("Incident UID."),
		schemaTypeNumber:    intProp("Short per-org reference, rendered as #42."),
		propCheckUID:        stringProp("UID of the check the incident belongs to."),
		propKind:            stringProp("What the incident is about: check, slo_burn or degraded."),
		propState:           stringProp("Incident state, e.g. active or resolved."),
		schemaKeyStartedAt:  stringProp("RFC3339 timestamp of the incident's onset."),
		schemaKeyResolvedAt: stringProp("RFC3339 resolution timestamp; absent while the incident is open."),
	}
}

// DiagnoseCheckResult is the JSON shape returned by the diagnose_check tool.
type DiagnoseCheckResult struct {
	Check checks.CheckResponse `json:"check"`
	// Freshness names every region that has gone silent, in words ("no result
	// from lauterbourg since 13:41, 2 other regions reporting"). RecentResults
	// is trimmed per region, so a dead region would otherwise simply be absent
	// from it — and absence is the one thing a reader never notices (spec
	// 2026-09-25-02). Empty when every region is reporting.
	Freshness []string `json:"freshness,omitempty"`
	// RegionalIssue spells out the check's regional issue (spec
	// 2026-09-25-10): some, but fewer than the quorum, of its regions failing —
	// the check reads `warning` and no incident opens. Empty otherwise.
	RegionalIssue        string                      `json:"regionalIssue,omitempty"`
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
		IncludeRegionFreshness:  true,
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
		With:        []string{schemaKeyRegion, "output", "durationMs"},
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
		Freshness:            describeFreshness(check),
		RegionalIssue:        describeRegionalIssue(check),
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

// describeRegionalIssue renders the regional-issue block as one sentence.
func describeRegionalIssue(check *checks.CheckResponse) string {
	issue := check.RegionalIssue
	if issue == nil {
		return ""
	}

	return fmt.Sprintf(
		"regional issue: failing from %s (%d of %d regions); an incident opens only when %d region(s) "+
			"fail for the confirmation period",
		strings.Join(issue.FailingRegions, ", "), len(issue.FailingRegions), issue.RegionCount, issue.FailQuorum,
	)
}

// describeFreshness turns the check's per-region freshness into sentences, one
// per silent region, each saying how many other regions are still reporting.
// A stale check with no region breakdown still gets one line.
func describeFreshness(check *checks.CheckResponse) []string {
	silent := make([]checks.RegionFreshnessResponse, 0, len(check.RegionFreshness))
	reporting := 0

	for i := range check.RegionFreshness {
		if check.RegionFreshness[i].Stale {
			silent = append(silent, check.RegionFreshness[i])
		} else {
			reporting++
		}
	}

	out := make([]string, 0, len(silent)+1)

	for i := range silent {
		region := &silent[i]
		name := region.Region
		if name == "" {
			name = "the default region"
		}

		since := "within the raw retention window"
		if region.LastResultAt != nil {
			since = "since " + region.LastResultAt.UTC().Format(time.RFC3339)
		}

		out = append(out, fmt.Sprintf("no result from %s %s, %d other region(s) reporting",
			name, since, reporting))
	}

	if len(out) == 0 && check.Status == models.WireStatusStale {
		since := "ever"
		if check.LastResultAt != nil {
			since = "since " + check.LastResultAt.UTC().Format(time.RFC3339)
		}

		out = append(out, "no result from any region "+since)
	}

	return out
}
