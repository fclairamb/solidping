package regions_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/regions"
)

func TestIsPrivateRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.True(regions.IsPrivateRegion("@acme/dc1"))
	r.True(regions.IsPrivateRegion("@x"))
	r.False(regions.IsPrivateRegion("eu-west-1"))
	r.False(regions.IsPrivateRegion("default"))
	r.False(regions.IsPrivateRegion(""))
}

func TestPrivateRegionSlugRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	full := regions.PrivateRegionSlug("acme", "dc1")
	r.Equal("@acme/dc1", full)

	org, reg, ok := regions.ParsePrivateRegion(full)
	r.True(ok)
	r.Equal("acme", org)
	r.Equal("dc1", reg)
}

func TestParsePrivateRegionRejectsMalformed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	for _, in := range []string{"eu-west-1", "@acme", "@/dc1", "@acme/", "@", "acme/dc1"} {
		_, _, ok := regions.ParsePrivateRegion(in)
		r.Falsef(ok, "expected %q to be rejected", in)
	}
}

func TestValidatePrivateRegionSlug(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.NoError(regions.ValidatePrivateRegionSlug("dc1"))
	r.NoError(regions.ValidatePrivateRegionSlug("on-prem-paris"))

	for _, bad := range []string{"", "a", "DC1", "1dc", "dc_1", "dc/1", "@dc", "dc 1"} {
		r.Errorf(regions.ValidatePrivateRegionSlug(bad), "expected %q to be invalid", bad)
	}
}

// TestMatchesRegionPrivateIsExactOnly is the core security invariant: a private
// (`@…`) job region matches ONLY on exact equality — never by prefix — so a
// cloud worker can never prefix-match its way into a private-region job.
func TestMatchesRegionPrivateIsExactOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Cloud prefix matching still works.
	r.True(regions.MatchesRegion("eu-west-1", "eu"))
	r.True(regions.MatchesRegion("default", "default"))
	r.False(regions.MatchesRegion("us-east-1", "eu"))

	// A cloud worker can never match a private job region.
	r.False(regions.MatchesRegion("eu-west-1", "@acme/dc1"))
	r.False(regions.MatchesRegion("default", "@acme/dc1"))
	r.False(regions.MatchesRegion("@acme", "@acme/dc1")) // no prefix matching for private

	// Exact private match is the only thing that returns true.
	r.True(regions.MatchesRegion("@acme/dc1", "@acme/dc1"))
	r.False(regions.MatchesRegion("@acme/dc1", "@acme/dc2"))
	// A private worker string never leaks into a cloud job by prefix.
	r.False(regions.MatchesRegion("@acme/dc1", "@acme"))
}

// TestValidateWorkerRegionRejectsPrivatePrefix asserts a cloud/in-process worker
// can never be configured with a private ('@') region. The reserved-prefix check
// short-circuits before any DB lookup, so a nil-db service is sufficient here.
func TestValidateWorkerRegionRejectsPrivatePrefix(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := regions.NewService(nil)

	err := svc.ValidateWorkerRegion(t.Context(), "@acme/dc1")
	r.ErrorIs(err, regions.ErrPrivateRegionReserved)
}
