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
	r.Equal("object", schema["type"])

	props, ok := schema["properties"].(map[string]any)
	r.True(ok)
	r.Contains(props, propIdentifier)
	r.Contains(props, propRecentResultsLimit)

	required, ok := schema["required"].([]string)
	r.True(ok)
	r.Equal([]string{propIdentifier}, required)
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
