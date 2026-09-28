package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/results"
)

func TestDiagnoseCheckDef(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	def := diagnoseCheckDef()
	r.Equal("diagnose_check", def.Name)
	r.NotEmpty(def.Description)

	schema, ok := def.InputSchema.(map[string]any)
	r.True(ok)
	r.Equal(schemaTypeObject, schema[schemaKeyType])

	props, ok := schema[schemaKeyProperties].(map[string]any)
	r.True(ok)
	r.Contains(props, propIdentifier)
	r.Contains(props, propRecentResultsLimit)

	required, ok := schema["required"].([]string)
	r.True(ok)
	r.Equal([]string{propIdentifier}, required)
}

// TestDiagnoseCheckDef_AnnotationsAndOutputSchema: diagnose_check is
// read-only and its outputSchema mirrors DiagnoseCheckResult — with the two
// nullable incidents declared as object-or-null so a null satisfies it.
func TestDiagnoseCheckDef_AnnotationsAndOutputSchema(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	def := diagnoseCheckDef()

	r.NotNil(def.Annotations)
	r.True(def.Annotations.ReadOnlyHint)
	r.False(def.Annotations.DestructiveHint)
	r.True(def.Annotations.IdempotentHint)
	r.False(def.Annotations.OpenWorldHint)
	r.Equal("Diagnose check", def.Annotations.Title)
	r.Contains(def.Description, "Read-only: works with mcp:read tokens.")

	schema, ok := def.OutputSchema.(map[string]any)
	r.True(ok)
	r.Equal(schemaTypeObject, schema[schemaKeyType])

	props, ok := schema[schemaKeyProperties].(map[string]any)
	r.True(ok)

	for _, key := range []string{
		schemaKeyCheck, "freshness", "regionalIssue", "recentResults",
		"activeIncident", "lastResolvedIncident",
	} {
		r.Contains(props, key)
	}

	required, ok := schema["required"].([]string)
	r.True(ok)
	r.ElementsMatch(
		[]string{schemaKeyCheck, "recentResults", "activeIncident", "lastResolvedIncident"},
		required,
	)

	// The check object reuses the shared check output schema.
	checkProp, ok := props[schemaKeyCheck].(map[string]any)
	r.True(ok)
	r.Equal(schemaTypeObject, checkProp[schemaKeyType])
	checkProps, ok := checkProp[schemaKeyProperties].(map[string]any)
	r.True(ok)
	r.Contains(checkProps, propUID)

	for _, key := range []string{"activeIncident", "lastResolvedIncident"} {
		incidentProp, iOK := props[key].(map[string]any)
		r.True(iOK)
		incidentType, tOK := incidentProp[schemaKeyType].([]string)
		r.True(tOK, "%s must allow null", key)
		r.ElementsMatch([]string{schemaTypeObject, schemaTypeNull}, incidentType)
	}

	recent, ok := props["recentResults"].(map[string]any)
	r.True(ok)
	recentItems, ok := recent[schemaKeyItems].(map[string]any)
	r.True(ok)
	recentProps, ok := recentItems[schemaKeyProperties].(map[string]any)
	r.True(ok)
	r.Contains(recentProps, schemaKeyRegion)
	r.Contains(recentProps, propStatus)
}

func TestClampPerRegion(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	tests := []struct {
		name   string
		input  int
		expect int
	}{
		{"zero coerced to 1", 0, 1},
		{"negative coerced to 1", -5, 1},
		{"in range", 5, 5},
		{"max passes through", diagnoseMaxRecentResults, diagnoseMaxRecentResults},
		{"above max clamped", 999, diagnoseMaxRecentResults},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r.Equal(tc.expect, clampPerRegion(tc.input))
		})
	}
}

func TestTrimResultsPerRegion(t *testing.T) {
	t.Parallel()

	region1 := "eu-west-1"
	region2 := "us-east-1"

	mk := func(region *string, periodStart time.Time) results.ResultResponse {
		return results.ResultResponse{
			UID:         "r-" + periodStart.Format(time.RFC3339Nano),
			Region:      region,
			PeriodStart: periodStart,
			Status:      "down",
		}
	}

	base := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	in := []results.ResultResponse{
		mk(&region1, base),
		mk(&region2, base.Add(-1*time.Minute)),
		mk(&region1, base.Add(-2*time.Minute)),
		mk(&region2, base.Add(-3*time.Minute)),
		mk(&region1, base.Add(-4*time.Minute)),
		mk(&region2, base.Add(-5*time.Minute)),
	}

	t.Run("limit 1 keeps newest per region", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		out := trimResultsPerRegion(in, 1)
		r.Len(out, 2)
		r.Equal(in[0].UID, out[0].UID)
		r.Equal(in[1].UID, out[1].UID)
	})

	t.Run("limit 2 keeps two newest per region in original order", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		out := trimResultsPerRegion(in, 2)
		r.Len(out, 4)
		r.Equal(in[0].UID, out[0].UID)
		r.Equal(in[1].UID, out[1].UID)
		r.Equal(in[2].UID, out[2].UID)
		r.Equal(in[3].UID, out[3].UID)
	})

	t.Run("limit larger than available is a no-op", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		out := trimResultsPerRegion(in, 10)
		r.Len(out, len(in))
	})

	t.Run("empty input returns empty slice", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		out := trimResultsPerRegion(nil, 5)
		r.Empty(out)
	})

	t.Run("zero limit returns empty slice", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		out := trimResultsPerRegion(in, 0)
		r.Empty(out)
	})

	t.Run("nil region key groups together", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		mixed := []results.ResultResponse{
			mk(nil, base),
			mk(nil, base.Add(-1*time.Minute)),
			mk(nil, base.Add(-2*time.Minute)),
		}
		out := trimResultsPerRegion(mixed, 2)
		r.Len(out, 2)
	})
}

func TestBuildDiagnoseResponse(t *testing.T) {
	t.Parallel()

	region := "eu-west-1"
	check := checks.CheckResponse{
		UID: "check-1",
		Slug: func() *string {
			s := "api-prod"
			return &s
		}(),
		Regions: []string{region},
	}

	t.Run("no incidents, results pass through trimmed", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		recent := []results.ResultResponse{
			{UID: "r1", Region: &region},
			{UID: "r2", Region: &region},
			{UID: "r3", Region: &region},
		}

		out := buildDiagnoseResponse(&check, recent, nil, nil, 2)
		r.Equal("check-1", out.Check.UID)
		r.Len(out.RecentResults, 2)
		r.Nil(out.ActiveIncident)
		r.Nil(out.LastResolvedIncident)
	})

	t.Run("active incident present, no resolved", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		active := &incidents.IncidentResponse{UID: "inc-active", State: "active"}
		out := buildDiagnoseResponse(&check, nil, active, nil, 5)
		r.NotNil(out.ActiveIncident)
		r.Equal("inc-active", out.ActiveIncident.UID)
		r.Nil(out.LastResolvedIncident)
	})

	t.Run("both incidents present", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		active := &incidents.IncidentResponse{UID: "inc-active", State: "active"}
		resolved := &incidents.IncidentResponse{UID: "inc-resolved", State: "resolved"}
		out := buildDiagnoseResponse(&check, nil, active, resolved, 5)
		r.NotNil(out.ActiveIncident)
		r.Equal("inc-active", out.ActiveIncident.UID)
		r.NotNil(out.LastResolvedIncident)
		r.Equal("inc-resolved", out.LastResolvedIncident.UID)
	})
}

// TestDiagnoseCheck_MissingIdentifier exercises the handler entry point's
// argument validation without needing service stubs (the call returns before
// touching any service).
func TestDiagnoseCheck_MissingIdentifier(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	result := handler.toolDiagnoseCheck(context.Background(), "test-org", map[string]any{})
	r.True(result.IsError)
	r.Len(result.Content, 1)
	r.Contains(result.Content[0].Text, "identifier is required")
}

// A dead region must be NAMED, not merely absent from the per-region trimmed
// results (spec 2026-09-25-02).
func TestDescribeFreshness(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	lastSeen := time.Date(2026, 9, 24, 13, 41, 0, 0, time.UTC)
	fresh := time.Date(2026, 9, 24, 21, 40, 0, 0, time.UTC)

	check := &checks.CheckResponse{
		Status: "up",
		RegionFreshness: []checks.RegionFreshnessResponse{
			{Region: "eu-west", LastResultAt: &fresh},
			{Region: "lauterbourg", LastResultAt: &lastSeen, Stale: true},
			{Region: "us-east", LastResultAt: &fresh},
		},
	}

	r.Equal([]string{
		"no result from lauterbourg since 2026-09-24T13:41:00Z, 2 other region(s) reporting",
	}, describeFreshness(check))

	// Every region reporting: nothing to say.
	check.RegionFreshness[1] = checks.RegionFreshnessResponse{Region: "lauterbourg", LastResultAt: &fresh}
	r.Empty(describeFreshness(check))

	// A stale check with no region breakdown still says so.
	r.Equal([]string{"no result from any region since 2026-09-24T13:41:00Z"},
		describeFreshness(&checks.CheckResponse{Status: "stale", LastResultAt: &lastSeen}))
}

// The regional issue (spec 2026-09-25-10) is spelled out, and absent when
// there is none.
func TestDescribeRegionalIssue(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Empty(describeRegionalIssue(&checks.CheckResponse{Status: "up"}))

	r.Equal("regional issue: failing from tokyo (1 of 3 regions); an incident opens only when 2 region(s) "+
		"fail for the confirmation period",
		describeRegionalIssue(&checks.CheckResponse{
			Status: "warning",
			RegionalIssue: &checks.RegionalIssueResponse{
				FailingRegions: []string{"tokyo"}, FailQuorum: 2, RegionCount: 3,
			},
		}))
}
