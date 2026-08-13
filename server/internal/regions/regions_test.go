package regions_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/regions"
)

func TestIsPrivateRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.True(regions.IsPrivateRegion("@dc1"))
	r.True(regions.IsPrivateRegion("@acme/dc1")) // the legacy spelling is still "private"
	r.True(regions.IsPrivateRegion("@x"))
	r.False(regions.IsPrivateRegion("eu-west-1"))
	r.False(regions.IsPrivateRegion("default"))
	r.False(regions.IsPrivateRegion(""))
}

// TestPrivateRegionSlugIsOrgRelative pins the shape the whole spec turns on:
// the stored string carries the region slug and NOTHING else, so renaming the
// org cannot invalidate it.
func TestPrivateRegionSlugIsOrgRelative(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	full := regions.PrivateRegionSlug("dc1")
	r.Equal("@dc1", full)

	reg, ok := regions.ParsePrivateRegion(full)
	r.True(ok)
	r.Equal("dc1", reg)
}

func TestParsePrivateRegionRejectsMalformed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	// The legacy fully-qualified spelling is NOT an org-relative region — it
	// must go through ParseLegacyPrivateRegion (and an org check) instead.
	for _, in := range []string{"eu-west-1", "@acme/dc1", "@/dc1", "@acme/", "@", "acme/dc1"} {
		_, ok := regions.ParsePrivateRegion(in)
		r.Falsef(ok, "expected %q to be rejected", in)
	}
}

func TestParseLegacyPrivateRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	org, reg, ok := regions.ParseLegacyPrivateRegion("@acme/dc1")
	r.True(ok)
	r.Equal("acme", org)
	r.Equal("dc1", reg)

	// Only the legacy two-segment shape parses; the org-relative form does not
	// (it has no org to check against), and neither do malformed variants.
	for _, in := range []string{"@dc1", "@", "@/dc1", "@acme/", "@a/b/c", "eu-west-1", ""} {
		_, _, parsed := regions.ParseLegacyPrivateRegion(in)
		r.Falsef(parsed, "expected %q to be rejected", in)
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

	// A cloud worker can never match a private job region — and the org-relative
	// form is the interesting new case, because `@aws-paris` is now short enough
	// that a sloppier matcher might have let something prefix into it.
	r.False(regions.MatchesRegion("eu-west-1", "@aws-paris"))
	r.False(regions.MatchesRegion("default", "@aws-paris"))
	r.False(regions.MatchesRegion("aws-paris", "@aws-paris"))
	r.False(regions.MatchesRegion("@aws", "@aws-paris")) // no prefix matching for private

	// Exact private match is the only thing that returns true.
	r.True(regions.MatchesRegion("@aws-paris", "@aws-paris"))
	r.False(regions.MatchesRegion("@aws-paris", "@aws-lyon"))
	// A private worker string never leaks into a cloud job by prefix.
	r.False(regions.MatchesRegion("@aws-paris", "@aws"))
	// The legacy spelling and the org-relative one are DIFFERENT strings; only
	// the migration collapses them, never the matcher.
	r.False(regions.MatchesRegion("@acme/aws-paris", "@aws-paris"))
}

// TestValidateWorkerRegionRejectsPrivatePrefix asserts a cloud/in-process worker
// can never be configured with a private ('@') region. The reserved-prefix check
// short-circuits before any DB lookup, so a nil-db service is sufficient here.
func TestValidateWorkerRegionRejectsPrivatePrefix(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := regions.NewService(nil)

	// Every `@…` shape is refused, org-relative and legacy alike — the prefix is
	// reserved, not the format.
	for _, bad := range []string{"@dc1", "@acme/dc1", "@", "@anything"} {
		err := svc.ValidateWorkerRegion(t.Context(), bad)
		r.ErrorIsf(err, regions.ErrPrivateRegionReserved, "worker region %q must be refused", bad)
	}
}
