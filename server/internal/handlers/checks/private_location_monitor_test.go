package checks

import (
	"testing"

	"github.com/stretchr/testify/require"

	plconfig "github.com/fclairamb/solidping/server/internal/checkers/checkprivatelocation/config"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// TestPrivateLocationRegionShapeAgreesWithRegions pins the one rule the light
// config package duplicates (to stay free of the regions service): the shape
// of an org-relative private region.
func TestPrivateLocationRegionShapeAgreesWithRegions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, slug := range []string{
		"office", "ab", "a1", "dc-1", "a", "1office", "Office", "off_ice", "off/ice",
		"a23456789012345678901234567890", "a234567890123456789012345678901", "",
	} {
		region := regions.PrivateRegionSlug(slug)
		want := regions.ValidatePrivateRegionSlug(slug) == nil

		r.Equalf(want, plconfig.IsPrivateRegion(region), "slug %q", slug)

		if want {
			parsed, ok := regions.ParsePrivateRegion(region)
			r.True(ok)
			r.Equal(parsed, plconfig.RegionSlug(region))
		}
	}
}
