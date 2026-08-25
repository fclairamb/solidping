package results

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// periodTypeHour is the aggregation-level PeriodType used by the
// PeriodTypeIsolation and Fallback test fixtures below.
const periodTypeHour = "hour"

// newNeighborsTestDB builds an in-memory SQLite-backed db.Service for the
// GetResult/GetResultNeighbors tests below.
func newNeighborsTestDB(t *testing.T) (db.Service, context.Context) {
	t.Helper()

	ctx := t.Context()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	t.Cleanup(func() { _ = svc.Close() })

	require.NoError(t, svc.Initialize(ctx))

	return svc, ctx
}

// newBareCheck creates a check and immediately deletes the "Check created"
// raw marker row that CreateCheck seeds automatically (real product
// behavior — every new check gets one at the actual current wall-clock
// time). Left in place it would pollute these tests' synthetic timelines
// (which use a fixed base date) with an extra real-"now" raw row.
func newBareCheck(ctx context.Context, dbSvc db.Service, r *require.Assertions, orgUID, slug string) *models.Check {
	check := models.NewCheck(orgUID, slug, "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	seeded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       []string{check.UID},
		Limit:           10,
	})
	r.NoError(err)

	uids := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		uids = append(uids, row.UID)
	}

	if len(uids) > 0 {
		_, err := dbSvc.DeleteResults(ctx, orgUID, uids)
		r.NoError(err)
	}

	return check
}

func TestGetResultNeighbors_SQLite(t *testing.T) {
	t.Parallel()

	dbSvc, ctx := newNeighborsTestDB(t)
	testGetResultNeighborsAcrossBackend(ctx, t, dbSvc)
}

// testGetResultNeighborsAcrossBackend is the shared assertion body run
// against both SQLite (directly, above) and embedded Postgres (from
// service_neighbors_postgres_test.go), proving GetResult's previousUid/
// nextUid computation (spec 2026-07-05-14) behaves identically on both
// backends: middle/oldest/newest rows, periodType isolation, other-check and
// other-org isolation, region scoping, same-period_start tie-break, and the
// fallback (rolled-up) case.
func testGetResultNeighborsAcrossBackend(ctx context.Context, t *testing.T, dbSvc db.Service) {
	t.Helper()

	r := require.New(t)
	svc := NewService(dbSvc, nil)

	org := models.NewOrganization("nbr-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := newBareCheck(ctx, dbSvc, r, org.UID, "neighbors-check")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	newRawAt := func(offset time.Duration, region *string) *models.Result {
		res := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0.1)
		res.PeriodStart = base.Add(offset)
		res.Region = region
		r.NoError(dbSvc.CreateResult(ctx, res))

		return res
	}

	t.Run("MiddleOldestNewest", func(t *testing.T) {
		t.Parallel()

		oldest := newRawAt(1*time.Minute, nil)
		middle := newRawAt(2*time.Minute, nil)
		newest := newRawAt(3*time.Minute, nil)

		resp, err := svc.GetResult(ctx, org.Slug, check.UID, middle.UID, nil)
		r.NoError(err)
		r.Equal(oldest.UID, resp.PreviousUID, "middle row's previous must be the oldest")
		r.Equal(newest.UID, resp.NextUID, "middle row's next must be the newest")

		respOldest, err := svc.GetResult(ctx, org.Slug, check.UID, oldest.UID, nil)
		r.NoError(err)
		r.Empty(respOldest.PreviousUID, "oldest row has no older neighbor")
		r.Equal(middle.UID, respOldest.NextUID)

		respNewest, err := svc.GetResult(ctx, org.Slug, check.UID, newest.UID, nil)
		r.NoError(err)
		r.Equal(middle.UID, respNewest.PreviousUID)
		r.Empty(respNewest.NextUID, "newest row has no newer neighbor")
	})

	t.Run("PeriodTypeIsolation", func(t *testing.T) {
		t.Parallel()

		ptOrg := models.NewOrganization("nbr-pt-org", "")
		r.NoError(dbSvc.CreateOrganization(ctx, ptOrg))
		ptCheck := newBareCheck(ctx, dbSvc, r, ptOrg.UID, "neighbors-pt-check")

		rawA := models.NewResult(ptOrg.UID, ptCheck.UID, models.ResultStatusUp, 0.1)
		rawA.PeriodStart = base.Add(10 * time.Minute)
		r.NoError(dbSvc.CreateResult(ctx, rawA))

		// An hour-aggregation row sitting chronologically between rawA and
		// rawB must NOT be picked up as a neighbor of either raw row — the
		// scope is same check + periodType as the effective row.
		hourAgg := models.NewResult(ptOrg.UID, ptCheck.UID, models.ResultStatusUp, 0.1)
		hourAgg.PeriodType = periodTypeHour
		hourAgg.PeriodStart = base.Add(15 * time.Minute)
		hourAgg.Status = nil
		hourAgg.Duration = nil
		r.NoError(dbSvc.CreateResult(ctx, hourAgg))

		rawB := models.NewResult(ptOrg.UID, ptCheck.UID, models.ResultStatusUp, 0.1)
		rawB.PeriodStart = base.Add(20 * time.Minute)
		r.NoError(dbSvc.CreateResult(ctx, rawB))

		resp, err := svc.GetResult(ctx, ptOrg.Slug, ptCheck.UID, rawA.UID, nil)
		r.NoError(err)
		r.Empty(resp.PreviousUID)
		r.Equal(rawB.UID, resp.NextUID, "next must skip the hour-periodType row and land on the next raw row")
	})

	t.Run("OtherCheckAndOrgIsolation", func(t *testing.T) {
		t.Parallel()

		isoOrg := models.NewOrganization("nbr-iso-org", "")
		r.NoError(dbSvc.CreateOrganization(ctx, isoOrg))
		checkA := newBareCheck(ctx, dbSvc, r, isoOrg.UID, "neighbors-iso-check-a")
		checkB := newBareCheck(ctx, dbSvc, r, isoOrg.UID, "neighbors-iso-check-b")

		otherOrg := models.NewOrganization("nbr-other-org", "")
		r.NoError(dbSvc.CreateOrganization(ctx, otherOrg))
		checkC := newBareCheck(ctx, dbSvc, r, otherOrg.UID, "neighbors-other-check")

		// Same check_uid slug pattern across different checks/orgs, all at
		// the same instants, so a broken scope would find false neighbors.
		pivot := models.NewResult(isoOrg.UID, checkA.UID, models.ResultStatusUp, 0.1)
		pivot.PeriodStart = base.Add(30 * time.Minute)
		r.NoError(dbSvc.CreateResult(ctx, pivot))

		otherCheckRow := models.NewResult(isoOrg.UID, checkB.UID, models.ResultStatusUp, 0.1)
		otherCheckRow.PeriodStart = base.Add(29 * time.Minute)
		r.NoError(dbSvc.CreateResult(ctx, otherCheckRow))

		otherOrgRow := models.NewResult(otherOrg.UID, checkC.UID, models.ResultStatusUp, 0.1)
		otherOrgRow.PeriodStart = base.Add(31 * time.Minute)
		r.NoError(dbSvc.CreateResult(ctx, otherOrgRow))

		resp, err := svc.GetResult(ctx, isoOrg.Slug, checkA.UID, pivot.UID, nil)
		r.NoError(err)
		r.Empty(resp.PreviousUID, "another check's row must not leak in as previous")
		r.Empty(resp.NextUID, "another org's row must not leak in as next")
	})

	t.Run("RegionScoping", func(t *testing.T) {
		t.Parallel()

		regOrg := models.NewOrganization("nbr-region-org", "")
		r.NoError(dbSvc.CreateOrganization(ctx, regOrg))
		regCheck := newBareCheck(ctx, dbSvc, r, regOrg.UID, "neighbors-region-check")

		euRegion, usRegion := "eu", "us"

		euOlder := models.NewResult(regOrg.UID, regCheck.UID, models.ResultStatusUp, 0.1)
		euOlder.PeriodStart = base.Add(40 * time.Minute)
		euOlder.Region = &euRegion
		r.NoError(dbSvc.CreateResult(ctx, euOlder))

		usMiddle := models.NewResult(regOrg.UID, regCheck.UID, models.ResultStatusUp, 0.1)
		usMiddle.PeriodStart = base.Add(41 * time.Minute)
		usMiddle.Region = &usRegion
		r.NoError(dbSvc.CreateResult(ctx, usMiddle))

		euNewer := models.NewResult(regOrg.UID, regCheck.UID, models.ResultStatusUp, 0.1)
		euNewer.PeriodStart = base.Add(42 * time.Minute)
		euNewer.Region = &euRegion
		r.NoError(dbSvc.CreateResult(ctx, euNewer))

		// Unfiltered: neighbors are chronological across all regions.
		unfiltered, err := svc.GetResult(ctx, regOrg.Slug, regCheck.UID, usMiddle.UID, nil)
		r.NoError(err)
		r.Equal(euOlder.UID, unfiltered.PreviousUID)
		r.Equal(euNewer.UID, unfiltered.NextUID)

		// Filtered to eu: usMiddle itself is still resolved by UID (region
		// filter never blocks fetching the row itself), but its neighbors
		// jump across it directly from euOlder to euNewer — checked in both
		// directions for a full round-trip proof.
		filteredFromOlder, err := svc.GetResult(ctx, regOrg.Slug, regCheck.UID, euOlder.UID, []string{"eu"})
		r.NoError(err)
		r.Empty(filteredFromOlder.PreviousUID)
		r.Equal(euNewer.UID, filteredFromOlder.NextUID, "us row must be skipped forward when region=eu")

		filteredFromNewer, err := svc.GetResult(ctx, regOrg.Slug, regCheck.UID, euNewer.UID, []string{"eu"})
		r.NoError(err)
		r.Equal(euOlder.UID, filteredFromNewer.PreviousUID, "us row must be skipped backward when region=eu")
		r.Empty(filteredFromNewer.NextUID)
	})

	t.Run("SameTimestampTieBreak", func(t *testing.T) {
		t.Parallel()

		tieOrg := models.NewOrganization("nbr-tie-org", "")
		r.NoError(dbSvc.CreateOrganization(ctx, tieOrg))
		tieCheck := newBareCheck(ctx, dbSvc, r, tieOrg.UID, "neighbors-tie-check")

		sameInstant := base.Add(50 * time.Minute)

		// Three rows sharing the exact same period_start (multi-region runs
		// landing in the same millisecond); their generated UUIDv7 uids give
		// a stable, monotonic tie-break order since NewResult mints them in
		// sequence.
		first := models.NewResult(tieOrg.UID, tieCheck.UID, models.ResultStatusUp, 0.1)
		first.PeriodStart = sameInstant
		r.NoError(dbSvc.CreateResult(ctx, first))

		second := models.NewResult(tieOrg.UID, tieCheck.UID, models.ResultStatusUp, 0.1)
		second.PeriodStart = sameInstant
		r.NoError(dbSvc.CreateResult(ctx, second))

		third := models.NewResult(tieOrg.UID, tieCheck.UID, models.ResultStatusUp, 0.1)
		third.PeriodStart = sameInstant
		r.NoError(dbSvc.CreateResult(ctx, third))

		// UUIDv7 uids sort in creation order for uids minted in sequence
		// within the same process; assert the actual order matches whatever
		// GetResultNeighbors reports, then step across all three with no
		// skip or repeat.
		ordered := []*models.Result{first, second, third}
		if ordered[0].UID > ordered[1].UID {
			ordered[0], ordered[1] = ordered[1], ordered[0]
		}

		if ordered[1].UID > ordered[2].UID {
			ordered[1], ordered[2] = ordered[2], ordered[1]
		}

		if ordered[0].UID > ordered[1].UID {
			ordered[0], ordered[1] = ordered[1], ordered[0]
		}

		respFirst, err := svc.GetResult(ctx, tieOrg.Slug, tieCheck.UID, ordered[0].UID, nil)
		r.NoError(err)
		r.Empty(respFirst.PreviousUID)
		r.Equal(ordered[1].UID, respFirst.NextUID, "stepping forward from the first tie must land on the second, no skip")

		respSecond, err := svc.GetResult(ctx, tieOrg.Slug, tieCheck.UID, ordered[1].UID, nil)
		r.NoError(err)
		r.Equal(ordered[0].UID, respSecond.PreviousUID, "stepping back from the second must land on the first, no repeat")
		r.Equal(ordered[2].UID, respSecond.NextUID)

		respThird, err := svc.GetResult(ctx, tieOrg.Slug, tieCheck.UID, ordered[2].UID, nil)
		r.NoError(err)
		r.Equal(ordered[1].UID, respThird.PreviousUID)
		r.Empty(respThird.NextUID)
	})

	t.Run("FallbackNeighborsFromCoveringAggregationSeries", func(t *testing.T) {
		t.Parallel()

		fbOrg := models.NewOrganization("nbr-fallback-org", "")
		r.NoError(dbSvc.CreateOrganization(ctx, fbOrg))
		fbCheck := newBareCheck(ctx, dbSvc, r, fbOrg.UID, "neighbors-fallback-check")

		// A raw UID whose embedded UUIDv7 timestamp will fall inside an hour
		// aggregation bucket; the raw row is intentionally never inserted,
		// simulating "already rolled up and deleted".
		deletedRawUID, err := uuid.NewV7()
		r.NoError(err)

		sec, nsec := deletedRawUID.Time().UnixTime()
		requestedAt := time.Unix(sec, nsec).UTC()
		hourStart := requestedAt.Truncate(time.Hour)
		hourEnd := hourStart.Add(time.Hour)

		newHourAgg := func(offset time.Duration) *models.Result {
			agg := models.NewResult(fbOrg.UID, fbCheck.UID, models.ResultStatusUp, 0)
			agg.PeriodType = periodTypeHour
			agg.PeriodStart = hourStart.Add(offset)
			periodEnd := agg.PeriodStart.Add(time.Hour)
			agg.PeriodEnd = &periodEnd
			agg.Status = nil
			agg.Duration = nil
			totalChecks := 60
			agg.TotalChecks = &totalChecks
			r.NoError(dbSvc.CreateResult(ctx, agg))

			return agg
		}

		olderHour := newHourAgg(-1 * time.Hour)
		// coveringHour spans exactly [hourStart, hourEnd) and must cover
		// requestedAt (hourStart <= requestedAt < hourEnd by construction).
		coveringHour := models.NewResult(fbOrg.UID, fbCheck.UID, models.ResultStatusUp, 0)
		coveringHour.PeriodType = periodTypeHour
		coveringHour.PeriodStart = hourStart
		coveringHour.PeriodEnd = &hourEnd
		coveringHour.Status = nil
		coveringHour.Duration = nil
		totalChecks := 60
		coveringHour.TotalChecks = &totalChecks
		r.NoError(dbSvc.CreateResult(ctx, coveringHour))
		newerHour := newHourAgg(1 * time.Hour)

		// A raw row chronologically inside the same window must be ignored
		// for neighbor purposes (periodType isolation applies here too).
		distractorRaw := models.NewResult(fbOrg.UID, fbCheck.UID, models.ResultStatusUp, 0.1)
		distractorRaw.PeriodStart = requestedAt
		r.NoError(dbSvc.CreateResult(ctx, distractorRaw))

		resp, err := svc.GetResult(ctx, fbOrg.Slug, fbCheck.UID, deletedRawUID.String(), nil)
		r.NoError(err)
		r.NotNil(resp.Fallback, "raw row was never inserted, must fall back to the covering aggregation")
		r.Equal(coveringHour.UID, resp.UID, "effective row is the covering hour aggregation")
		r.Equal(olderHour.UID, resp.PreviousUID,
			"fallback neighbors must come from the hour-periodType series, not the requested raw uid")
		r.Equal(newerHour.UID, resp.NextUID)
	})
}
