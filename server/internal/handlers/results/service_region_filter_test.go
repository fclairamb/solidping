package results

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// TestListResultsNormalizesLegacyRegionFilter covers the read-filter half of the
// backward-compatibility rule (spec 2026-08-13-01). `?region=` is an input path
// like any other: a bookmarked or shared link written before the change carries
// `@<org>/<slug>`, and once migration 012 has rewritten the rows that link
// would otherwise return an EMPTY series with nothing explaining why — the same
// silent-nothing failure this whole spec exists to remove.
//
// The org's current slug and every recorded previous slug are folded to the
// org-relative form; a foreign org slug is a hard error, not an empty chart.
func TestListResultsNormalizesLegacyRegionFilter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dbSvc, ctx := newNeighborsTestDB(t)

	// `acme`, formerly `acmetech` — the live incident's org.
	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.AddOrganizationPreviousSlug(ctx, org.UID, "acmetech"))

	other := models.NewOrganization("evilcorp", "Evil Corp")
	r.NoError(dbSvc.CreateOrganization(ctx, other))

	check := newBareCheck(ctx, dbSvc, r, org.UID, "private-check")

	// Two rows in the private region (post-migration spelling) and one in a
	// cloud region, so a filter that silently degraded to "no filter" would
	// return three and be caught.
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	seed := func(offset time.Duration, region string) {
		row := &models.Result{
			UID:             uuid.Must(uuid.NewV7()).String(),
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			PeriodType:      "raw",
			PeriodStart:     base.Add(offset),
			Region:          &region,
		}
		r.NoError(dbSvc.CreateResult(ctx, row))
	}

	seed(0, "@aws-paris")
	seed(time.Minute, "@aws-paris")
	seed(2*time.Minute, "eu-west-1")

	svc := NewService(dbSvc, nil)

	countFor := func(regionFilter []string) int {
		t.Helper()

		resp, err := svc.ListResults(ctx, org.Slug, &ListResultsOptions{Regions: regionFilter, Size: 50})
		r.NoError(err)

		return len(resp.Data)
	}

	// Baseline: the org-relative spelling selects exactly the two private rows.
	canonical := countFor([]string{"@aws-paris"})
	r.Equal(2, canonical, "the org-relative filter must select exactly the private-region rows")

	// The legacy spelling — current slug AND the recorded previous slug — must
	// return the SAME series, not an empty one.
	r.Equal(canonical, countFor([]string{"@acme/aws-paris"}),
		"the legacy spelling with the org's CURRENT slug must return the same series")
	r.Equal(canonical, countFor([]string{"@acmetech/aws-paris"}),
		"the legacy spelling with a PREVIOUS slug must return the same series")

	// Mixing both spellings of one region must not double-count or drop rows.
	r.Equal(canonical, countFor([]string{"@acmetech/aws-paris", "@aws-paris"}))

	// A foreign org slug is rejected outright — an explicit 400 beats an
	// unexplained empty chart, and it matches what a check write does.
	_, err := svc.ListResults(ctx, org.Slug, &ListResultsOptions{
		Regions: []string{"@evilcorp/aws-paris"}, Size: 50,
	})
	r.ErrorIs(err, regions.ErrForeignPrivateRegion)

	// Cloud regions are untouched by any of this.
	r.Equal(1, countFor([]string{"eu-west-1"}))
}

// TestGetResultNormalizesLegacyRegionFilter pins the same rule on the
// single-result endpoint, whose `?region=` narrows the previous/next neighbor
// scope: a legacy link must keep resolving neighbors instead of silently
// finding none, and a foreign org slug must be refused.
func TestGetResultNormalizesLegacyRegionFilter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dbSvc, ctx := newNeighborsTestDB(t)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.AddOrganizationPreviousSlug(ctx, org.UID, "acmetech"))

	check := newBareCheck(ctx, dbSvc, r, org.UID, "private-check")

	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	region := "@aws-paris"
	uids := make([]string, 0, 3)

	for i := range 3 {
		row := &models.Result{
			UID:             uuid.Must(uuid.NewV7()).String(),
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			PeriodType:      "raw",
			PeriodStart:     base.Add(time.Duration(i) * time.Minute),
			Region:          &region,
		}
		r.NoError(dbSvc.CreateResult(ctx, row))
		uids = append(uids, row.UID)
	}

	svc := NewService(dbSvc, nil)
	middle := uids[1]

	neighbors := func(regionFilter []string) (string, string) {
		t.Helper()

		resp, err := svc.GetResult(ctx, org.Slug, check.UID, middle, regionFilter)
		r.NoError(err)

		return resp.PreviousUID, resp.NextUID
	}

	prev, next := neighbors([]string{"@aws-paris"})
	r.Equal(uids[0], prev)
	r.Equal(uids[2], next)

	// Both legacy spellings resolve the identical neighbor pair. Without
	// normalization the filter matches no row and both come back empty.
	for _, legacy := range []string{"@acme/aws-paris", "@acmetech/aws-paris"} {
		legacyPrev, legacyNext := neighbors([]string{legacy})
		r.Equalf(prev, legacyPrev, "legacy %q must resolve the same previous neighbor", legacy)
		r.Equalf(next, legacyNext, "legacy %q must resolve the same next neighbor", legacy)
	}

	_, err := svc.GetResult(ctx, org.Slug, check.UID, middle, []string{"@evilcorp/aws-paris"})
	r.ErrorIs(err, regions.ErrForeignPrivateRegion)
}
