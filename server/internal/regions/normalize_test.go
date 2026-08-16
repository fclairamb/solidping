package regions_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// normalizeFixture spins up an org that has been renamed twice, plus a
// completely unrelated org, so both halves of the backward-compatibility rule
// (accept my own slugs, reject anybody else's) can be exercised.
type normalizeFixture struct {
	svc   *regions.Service
	dbSvc *sqlite.Service
	org   *models.Organization
	other *models.Organization
	ctx   context.Context //nolint:containedctx // test fixture convenience
}

func newNormalizeFixture(t *testing.T) *normalizeFixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	// `acme`, formerly `acmetech` and before that `acmev0` — the live
	// incident's shape, with two generations of previous slugs.
	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.AddOrganizationPreviousSlug(ctx, org.UID, "acmetech"))
	r.NoError(dbSvc.AddOrganizationPreviousSlug(ctx, org.UID, "acmev0"))

	other := models.NewOrganization("evilcorp", "Evil Corp")
	r.NoError(dbSvc.CreateOrganization(ctx, other))

	return &normalizeFixture{
		svc:   regions.NewService(dbSvc),
		dbSvc: dbSvc,
		org:   org,
		other: other,
		ctx:   ctx,
	}
}

// TestNormalizeAcceptsOwnCurrentAndPreviousSlugs is the backward-compatibility
// half of spec 2026-08-13-01: an API client still posting the legacy
// `@<org>/<slug>` spelling keeps working, for the org's current slug AND for
// every slug it used to answer on.
func TestNormalizeAcceptsOwnCurrentAndPreviousSlugs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newNormalizeFixture(t)

	for _, in := range []string{"@acme/aws-paris", "@acmetech/aws-paris", "@acmev0/aws-paris"} {
		out, err := f.svc.NormalizeRegionsForOrg(f.ctx, f.org.UID, []string{in})
		r.NoErrorf(err, "%q must be accepted", in)
		r.Equalf([]string{"@aws-paris"}, out, "%q must normalize to the org-relative form", in)
	}

	// Cloud regions and already-normalized private regions pass through
	// untouched, and the order of the set is preserved.
	out, err := f.svc.NormalizeRegionsForOrg(
		f.ctx, f.org.UID, []string{"eu-west-1", "@acmetech/aws-paris", "default"},
	)
	r.NoError(err)
	r.Equal([]string{"eu-west-1", "@aws-paris", "default"}, out)
}

// TestNormalizeRejectsForeignOrgSlug is the security half: silently stripping a
// foreign org would let one tenant name another tenant's region, so the
// rejection must be a hard error, not a fold.
func TestNormalizeRejectsForeignOrgSlug(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newNormalizeFixture(t)

	_, err := f.svc.NormalizeRegionsForOrg(f.ctx, f.org.UID, []string{"@evilcorp/aws-paris"})
	r.ErrorIs(err, regions.ErrForeignPrivateRegion)

	// Even a slug that belongs to NO org is refused — accepting unknown orgs
	// would make the check decorative.
	_, err = f.svc.NormalizeRegionsForOrg(f.ctx, f.org.UID, []string{"@nosuchorg/aws-paris"})
	r.ErrorIs(err, regions.ErrForeignPrivateRegion)

	// A foreign entry poisons the whole set: no partial acceptance.
	_, err = f.svc.NormalizeRegionsForOrg(
		f.ctx, f.org.UID, []string{"@aws-paris", "@evilcorp/aws-paris"},
	)
	r.ErrorIs(err, regions.ErrForeignPrivateRegion)

	// And the reverse direction holds: the OTHER org may not claim ours.
	_, err = f.svc.NormalizeRegionsForOrg(f.ctx, f.other.UID, []string{"@acmetech/aws-paris"})
	r.ErrorIs(err, regions.ErrForeignPrivateRegion)
}

// TestNormalizeCollapsesBothSpellingsToOneEntry pins the de-duplication.
// Two entries would create two check_jobs rows for one region, which the unique
// index on (check_uid, region) would then reject on write.
func TestNormalizeCollapsesBothSpellingsToOneEntry(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newNormalizeFixture(t)

	out, err := f.svc.NormalizeRegionsForOrg(f.ctx, f.org.UID, []string{
		"@acmetech/aws-paris", "@acme/aws-paris", "@aws-paris", "eu-west-1", "eu-west-1",
	})
	r.NoError(err)
	r.Equal([]string{"@aws-paris", "eu-west-1"}, out)
}

// TestResolveRegionsForCheckNormalizesCallerInput asserts the normalization is
// actually ON the path checks take, not just available as a helper.
func TestResolveRegionsForCheckNormalizesCallerInput(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newNormalizeFixture(t)

	out, err := f.svc.ResolveRegionsForCheck(f.ctx, []string{"@acmetech/aws-paris"}, f.org.UID)
	r.NoError(err)
	r.Equal([]string{"@aws-paris"}, out)

	_, err = f.svc.ResolveRegionsForCheck(f.ctx, []string{"@evilcorp/aws-paris"}, f.org.UID)
	r.ErrorIs(err, regions.ErrForeignPrivateRegion)
}

// TestResolveRegionsForCheckNormalizesStoredOrgDefaults covers the lenient
// path: a `default_regions` parameter migration 012 has not reached still
// resolves to the org-relative form rather than handing a stale string to a
// freshly-created check.
func TestResolveRegionsForCheckNormalizesStoredOrgDefaults(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newNormalizeFixture(t)

	r.NoError(f.dbSvc.SetOrgParameter(
		f.ctx, f.org.UID, regions.ParamDefaultRegions,
		[]string{"@acmetech/aws-paris", "@acme/aws-paris", "default"}, false,
	))

	out, err := f.svc.ResolveRegionsForCheck(f.ctx, nil, f.org.UID)
	r.NoError(err)
	r.Equal([]string{"@aws-paris", "default"}, out)
}
