package regions_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/regions"
)

// declared is the region list the placement tests share, in declaration
// order: four browser-capable EU regions and tokyo, which has no Chrome.
func declared() []regions.RegionDefinition {
	return []regions.RegionDefinition{
		{Slug: "gravelines"},
		{Slug: "lauterbourg"},
		{Slug: "nuremberg"},
		{Slug: "paris"},
		{Slug: "tokyo"},
	}
}

func capabilityIndex() map[string]regions.RegionDefinition {
	yes := map[string]string{regions.CapabilityBrowser: regions.CapabilityYes, regions.CapabilityIPv6: regions.CapabilityYes}

	return map[string]regions.RegionDefinition{
		"gravelines":  {Slug: "gravelines", Capabilities: yes},
		"lauterbourg": {Slug: "lauterbourg", Capabilities: yes},
		"nuremberg": {Slug: "nuremberg", Capabilities: map[string]string{
			regions.CapabilityBrowser: regions.CapabilityYes, regions.CapabilityIPv6: regions.CapabilityNo,
		}},
		"paris": {Slug: "paris", Capabilities: yes},
		"tokyo": {Slug: "tokyo", Capabilities: map[string]string{
			regions.CapabilityBrowser: regions.CapabilityNo, regions.CapabilityIPv6: regions.CapabilityYes,
		}},
	}
}

func allHealthy() map[string]bool {
	return map[string]bool{"gravelines": true, "lauterbourg": true, "nuremberg": true, "paris": true, "tokyo": true}
}

func TestCandidateOrder(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(
		[]string{"paris", "gravelines", "lauterbourg", "nuremberg", "tokyo"},
		regions.CandidateOrder([]string{"paris", "@private"}, []string{"gravelines", "paris"}, declared()),
		"org defaults, then system defaults, then the rest; private and duplicates dropped",
	)

	r.Equal(
		[]string{"gravelines", "lauterbourg", "nuremberg", "paris", "tokyo"},
		regions.CandidateOrder(nil, nil, declared()),
	)
}

func TestPlace(t *testing.T) {
	t.Parallel()

	base := regions.CandidateOrder(nil, []string{"gravelines"}, declared())

	cases := []struct {
		name  string
		input regions.PlacementInput
		want  []string
	}{
		{
			name:  "auto N=2, any pool: the first two healthy eligible in default order",
			input: regions.PlacementInput{Candidates: base, Healthy: allHealthy(), Count: 2},
			want:  []string{"gravelines", "lauterbourg"},
		},
		{
			name: "an unhealthy region is skipped in favor of the next healthy one",
			input: regions.PlacementInput{
				Candidates: base, Count: 2,
				Healthy: map[string]bool{"gravelines": true, "nuremberg": true, "paris": true},
			},
			want: []string{"gravelines", "nuremberg"},
		},
		{
			name: "a browser check skips a region with no headless Chrome",
			input: regions.PlacementInput{
				Candidates: []string{"tokyo", "paris", "gravelines"}, Count: 2,
				Required: []string{regions.CapabilityBrowser}, Capabilities: capabilityIndex(), Healthy: allHealthy(),
			},
			want: []string{"paris", "gravelines"},
		},
		{
			name: "an IPv6-only target skips a region without IPv6",
			input: regions.PlacementInput{
				Candidates: []string{"nuremberg", "paris"}, Count: 1,
				Required: []string{regions.CapabilityIPv6}, Capabilities: capabilityIndex(), Healthy: allHealthy(),
			},
			want: []string{"paris"},
		},
		{
			name: "an unknown capability stays eligible",
			input: regions.PlacementInput{
				Candidates: []string{"unreported", "paris"}, Count: 1,
				Required: []string{regions.CapabilityBrowser}, Capabilities: capabilityIndex(),
			},
			want: []string{"unreported"},
		},
		{
			name: "the pool restricts the candidates, keeping the base order",
			input: regions.PlacementInput{
				Candidates: base, Pool: []string{"paris", "nuremberg"}, Healthy: allHealthy(), Count: 2,
			},
			want: []string{"nuremberg", "paris"},
		},
		{
			name:  "N is capped by the number of eligible regions",
			input: regions.PlacementInput{Candidates: base, Pool: []string{"paris"}, Healthy: allHealthy(), Count: 3},
			want:  []string{"paris"},
		},
		{
			name:  "no health information at all: the plain candidate order",
			input: regions.PlacementInput{Candidates: base, Count: 2},
			want:  []string{"gravelines", "lauterbourg"},
		},
		{
			name: "short of healthy regions: fill with unhealthy eligible ones rather than shrink",
			input: regions.PlacementInput{
				Candidates: base, Count: 2, Healthy: map[string]bool{"paris": true},
			},
			want: []string{"paris", "gravelines"},
		},
		{
			name: "a still-eligible, healthy current region is kept (stability)",
			input: regions.PlacementInput{
				Candidates: base, Count: 2, Healthy: allHealthy(), Current: []string{"paris", "tokyo"},
			},
			want: []string{"paris", "tokyo"},
		},
		{
			name: "a current region that left the pool moves",
			input: regions.PlacementInput{
				Candidates: base, Count: 2, Healthy: allHealthy(),
				Current: []string{"paris", "tokyo"}, Pool: []string{"paris", "gravelines"},
			},
			want: []string{"paris", "gravelines"},
		},
		{
			name:  "no eligible region: empty",
			input: regions.PlacementInput{Candidates: base, Pool: []string{"mars"}, Count: 2},
			want:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			input := tc.input
			r.Equal(tc.want, regions.Place(&input))
			// Deterministic: the same input gives the same placement.
			r.Equal(tc.want, regions.Place(&input))
		})
	}
}

func TestReplace(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	candidates := []string{"gravelines", "lauterbourg", "nuremberg", "paris"}

	next, to, ok := regions.Replace(
		[]string{"lauterbourg", "gravelines"}, candidates,
		map[string]bool{"gravelines": true, "nuremberg": true, "paris": true}, "lauterbourg",
	)
	r.True(ok)
	r.Equal("nuremberg", to, "the next healthy candidate in order the check does not already use")
	r.Equal([]string{"nuremberg", "gravelines"}, next, "replaced in place")

	current := []string{"lauterbourg"}
	next, to, ok = regions.Replace(current, candidates, map[string]bool{}, "lauterbourg")
	r.False(ok, "no healthy candidate left")
	r.Empty(to)
	r.Equal(current, next, "the placement is kept")

	_, _, ok = regions.Replace([]string{"paris"}, candidates, allHealthy(), "lauterbourg")
	r.False(ok, "a region the check is not placed in moves nothing")
}
