package checks

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// The two engines word a violation of checks_slug_idx completely differently,
// and db.IsCheckSlugCollision is a string match, so both wordings are pinned
// here verbatim as the drivers emit them. The wordings themselves are proven
// against REAL databases by TestCheckSlugCollisionClassification_* in
// internal/db — these constants only make the loop's table below exhaustive
// and hermetic.
var (
	errPGSlugDuplicate = errors.New(
		`ERROR: duplicate key value violates unique constraint "checks_slug_idx" (SQLSTATE=23505)`)
	errSQLiteSlugDuplicate = errors.New(
		"UNIQUE constraint failed: checks.organization_uid, checks.slug")
)

// Unique violations that re-resolving a slug can NEVER fix. Creating a check
// writes its check_jobs and an initial result in the same transaction, so a
// violation from any of those arrives through exactly the same error path — and
// retrying it would just repeat the same failure a bounded number of times
// before surfacing it anyway, having burned the round trips.
var (
	errPGOtherDuplicate = errors.New(
		`ERROR: duplicate key value violates unique constraint "results_pkey" (SQLSTATE=23505)`)
	errSQLiteOtherDuplicate = errors.New(
		"UNIQUE constraint failed: check_jobs.check_uid")
)

// errSlugBoom is an ordinary failure — NOT a unique violation — used to prove
// the loop neither retries nor swallows anything else.
var errSlugBoom = errors.New("boom")

// newSlugCheck is a check carrying the slug the optimistic pre-read picked.
func newSlugCheck(slug string) *models.Check {
	check := models.NewCheck("org", slug, "http")

	return check
}

// slugOf reads back the slug the loop left on the check.
func slugOf(check *models.Check) string {
	if check.Slug == nil {
		return ""
	}

	return *check.Slug
}

// TestInsertResolvingSlugRace_RetriesTheLostSlug is the deterministic positive
// control behind the parallel race tests: whether two goroutines actually
// collide is up to the scheduler, so the retry is pinned here by forcing a
// violation on the first insert.
func TestInsertResolvingSlugRace_RetriesTheLostSlug(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	check := newSlugCheck("http-example-com")

	// The stand-in for the table: the free slug moves only once the winner's
	// row lands, which is exactly what makes the first, stale guess collide.
	taken := 1
	inserts := 0

	err := insertResolvingSlugRace(t.Context(), check, false,
		func(_ context.Context) error {
			inserts++

			if inserts == 1 {
				// Somebody else claimed http-example-com between our read and
				// our insert.
				taken = 2

				return errPGSlugDuplicate
			}

			return nil
		},
		func(_ context.Context) (string, error) {
			return "http-example-com-" + strconv.Itoa(taken), nil
		})

	r.NoError(err)
	r.Equal(2, inserts, "the collision must be retried exactly once")
	r.Equal("http-example-com-2", slugOf(check),
		"the retry must insert the RE-RESOLVED slug, not the stale one it just lost")
}

// TestInsertResolvingSlugRace_DoesNotRetryOtherErrors proves the loop is
// narrow: a real failure surfaces immediately instead of being hammered and
// reported later, which would bury the actual cause.
func TestInsertResolvingSlugRace_DoesNotRetryOtherErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	check := newSlugCheck("http-example-com")
	inserts := 0
	resolves := 0

	err := insertResolvingSlugRace(t.Context(), check, false,
		func(_ context.Context) error {
			inserts++

			return errSlugBoom
		},
		func(_ context.Context) (string, error) {
			resolves++

			return "never-used", nil
		})

	r.ErrorIs(err, errSlugBoom)
	r.Equal(1, inserts)
	r.Zero(resolves, "a non-slug failure must not trigger a re-resolve")
	r.Equal("http-example-com", slugOf(check), "a failed insert must not rewrite the slug")
}

// TestInsertResolvingSlugRace_OnlyRetriesTheSlugIndex is the efficiency
// contract, and the subtlest one here.
//
// The create transaction writes the check, its check_jobs and an initial
// result. A unique violation from any of those is not fixed by picking another
// slug, so a loop that matched "any unique violation" would spend its whole
// budget re-reading slugs before handing back an error the caller could have
// had on the first attempt.
func TestInsertResolvingSlugRace_OnlyRetriesTheSlugIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		violation   error
		wantRetried bool
	}{
		{name: "postgres results_pkey", violation: errPGOtherDuplicate},
		{name: "sqlite check_jobs columns", violation: errSQLiteOtherDuplicate},
		{
			// Positive controls: the SAME loop, the SAME error class, but on
			// the slug index — these MUST still be retried, or the two cases
			// above would also pass on a loop that retries nothing at all.
			name:        "postgres checks_slug_idx is still retried",
			violation:   errPGSlugDuplicate,
			wantRetried: true,
		},
		{
			name:        "sqlite checks.slug columns are still retried",
			violation:   errSQLiteSlugDuplicate,
			wantRetried: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			check := newSlugCheck("http-example-com")
			inserts := 0

			err := insertResolvingSlugRace(t.Context(), check, false,
				func(_ context.Context) error {
					inserts++

					if tt.wantRetried && inserts > 1 {
						return nil
					}

					return tt.violation
				},
				func(_ context.Context) (string, error) {
					return "http-example-com-2", nil
				})

			if tt.wantRetried {
				r.NoError(err)
				r.Equal(2, inserts)
				r.Equal("http-example-com-2", slugOf(check))

				return
			}

			r.ErrorIs(err, tt.violation,
				"a non-slug unique violation must surface as itself, on the first attempt")
			r.Equal(1, inserts)
			r.Equal("http-example-com", slugOf(check))
		})
	}
}

// TestInsertResolvingSlugRace_UserProvidedSlugIsNeverReResolved pins the
// promise that makes an explicit slug meaningful: storing something other than
// what the caller asked for would be a surprise, so a lost race becomes the
// same ErrSlugConflict (400) the pre-insert lookup returns when it sees the row
// — and the outcome stops depending on which side of the gap the conflict
// landed on.
func TestInsertResolvingSlugRace_UserProvidedSlugIsNeverReResolved(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	check := newSlugCheck("my-api")
	inserts := 0
	resolves := 0

	err := insertResolvingSlugRace(t.Context(), check, true,
		func(_ context.Context) error {
			inserts++

			return errSQLiteSlugDuplicate
		},
		func(_ context.Context) (string, error) {
			resolves++

			return "my-api-2", nil
		})

	r.ErrorIs(err, ErrSlugConflict)
	r.Equal(1, inserts, "an explicit slug gets exactly one attempt")
	r.Zero(resolves, "an explicit slug must never be re-resolved behind the caller's back")
	r.Equal("my-api", slugOf(check), "the caller's slug must come back untouched")
}

// TestInsertResolvingSlugRace_RidesOutMoreLostRacesThanTheStallBudget is the
// deterministic guard for the bug the parallel Postgres test caught: at 24
// simultaneous creations for one target it failed with a bare
// `checks_slug_idx` violation, because a fixed retry ceiling charged every
// legitimate lost race to the budget.
//
// A lost race means a competing creation committed, so the re-read returns a
// DIFFERENT slug and the next insert aims somewhere new. N racers make the
// unluckiest one lose up to N-1 times, and N belongs to the caller, not to this
// loop.
//
// Its counterpart is TestInsertResolvingSlugRace_GivesUpWhenTheSlugNeverMoves
// below, where the slug does NOT move and the loop must still terminate.
// Together they pin the distinction — a loop that simply retried more would
// pass this test and fail that one.
func TestInsertResolvingSlugRace_RidesOutMoreLostRacesThanTheStallBudget(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Comfortably more lost races than the stall budget, every one of them
	// legitimate contention.
	const lostRaces = checkSlugStallAttempts * 4

	check := newSlugCheck("http-example-com")
	inserts := 0
	free := 1

	err := insertResolvingSlugRace(t.Context(), check, false,
		func(_ context.Context) error {
			inserts++

			if inserts <= lostRaces {
				// Somebody else claimed the slug between our read and our
				// insert — which is what makes the next read return another.
				free++

				return errSQLiteSlugDuplicate
			}

			return nil
		},
		func(_ context.Context) (string, error) {
			return "http-example-com-" + strconv.Itoa(free), nil
		})

	r.NoError(err, "a lost race is contention the loop must ride out, not a livelock")
	r.Equal(lostRaces+1, inserts)
	r.Equal("http-example-com-"+strconv.Itoa(lostRaces+1), slugOf(check))
}

// TestInsertResolvingSlugRace_GivesUpWhenTheSlugNeverMoves proves the loop
// terminates. If the re-read keeps handing back the slug that just failed,
// every further insert fails identically, and spinning would hold the request
// open forever.
func TestInsertResolvingSlugRace_GivesUpWhenTheSlugNeverMoves(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	check := newSlugCheck("http-example-com")
	inserts := 0

	err := insertResolvingSlugRace(t.Context(), check, false,
		func(_ context.Context) error {
			inserts++

			return errSQLiteSlugDuplicate
		},
		func(_ context.Context) (string, error) {
			return "http-example-com", nil
		})

	r.ErrorIs(err, errSQLiteSlugDuplicate,
		"a stalled loop must surface the violation, not invent an error of its own")
	r.Equal(checkSlugStallAttempts, inserts)
}

// TestInsertResolvingSlugRace_SurfacesResolveFailures proves a broken re-read
// is reported rather than retried into the stall budget: if the database cannot
// answer "is this slug free?", inserting again is pointless.
func TestInsertResolvingSlugRace_SurfacesResolveFailures(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	check := newSlugCheck("http-example-com")
	inserts := 0

	err := insertResolvingSlugRace(t.Context(), check, false,
		func(_ context.Context) error {
			inserts++

			return errPGSlugDuplicate
		},
		func(_ context.Context) (string, error) {
			return "", errSlugBoom
		})

	r.ErrorIs(err, errSlugBoom)
	r.Equal(1, inserts)
}

// TestInsertResolvingSlugRace_SucceedsWithoutTouchingAnything is the boring
// baseline: the overwhelmingly common case must cost exactly one insert and no
// re-read at all.
func TestInsertResolvingSlugRace_SucceedsWithoutTouchingAnything(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	check := newSlugCheck("http-example-com")
	inserts := 0
	resolves := 0

	err := insertResolvingSlugRace(t.Context(), check, false,
		func(_ context.Context) error {
			inserts++

			return nil
		},
		func(_ context.Context) (string, error) {
			resolves++

			return "unused", nil
		})

	r.NoError(err)
	r.Equal(1, inserts)
	r.Zero(resolves)
	r.Equal("http-example-com", slugOf(check))
}
