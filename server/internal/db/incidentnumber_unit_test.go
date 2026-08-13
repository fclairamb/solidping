package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// errBoom is an ordinary failure — NOT a unique violation — used to prove the
// loop does not retry (or swallow) anything else.
var errBoom = errors.New("boom")

// The two engines word a unique-constraint violation completely differently,
// and the retry classifier is a string match, so both wordings are pinned here
// verbatim as the driver emits them.
var (
	errPostgresDuplicate = errors.New(
		`ERROR: duplicate key value violates unique constraint "incidents_organization_number_idx" (SQLSTATE=23505)`)
	errSQLiteDuplicate = errors.New(
		"UNIQUE constraint failed: incidents.organization_uid, incidents.number")
)

// TestCreateIncidentWithNumber_RetriesOnUniqueViolation is the deterministic
// positive control behind the parallel race test: whether two goroutines
// actually collide is up to the scheduler, so the retry is pinned here by
// forcing a unique violation on the first insert.
func TestCreateIncidentWithNumber_RetriesOnUniqueViolation(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// The stand-in for the table: MAX+1 grows only once the insert lands, which
	// is exactly what makes the first, stale guess collide.
	highest := int64(7)
	attempts := 0

	incident := models.NewIncident("org", "check", time.Now(), "collide")

	err := db.CreateIncidentWithNumber(t.Context(), incident,
		func(_ context.Context, _ string) (int64, error) { return highest + 1, nil },
		func(_ context.Context) error {
			attempts++

			if attempts == 1 {
				// Somebody else claimed #8 between our SELECT and our INSERT.
				highest = 8

				return errPostgresDuplicate
			}

			return nil
		})

	r.NoError(err)
	r.Equal(2, attempts, "the collision must be retried exactly once")
	r.EqualValues(9, incident.Number, "the retry must re-read MAX+1, not reuse the stale guess")
}

// TestCreateIncidentWithNumber_DoesNotRetryOtherErrors proves the loop is
// narrow: a real failure surfaces immediately instead of being hammered eight
// times and reported as "exhausted", which would bury the actual cause.
func TestCreateIncidentWithNumber_DoesNotRetryOtherErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	attempts := 0
	incident := models.NewIncident("org", "check", time.Now(), "boom")

	err := db.CreateIncidentWithNumber(t.Context(), incident,
		func(_ context.Context, _ string) (int64, error) { return 1, nil },
		func(_ context.Context) error {
			attempts++

			return errBoom
		})

	r.ErrorIs(err, errBoom)
	r.Equal(1, attempts)
	r.Zero(incident.Number, "a failed create must not leave a number that was never persisted")
}

// TestCreateIncidentWithNumber_GivesUpAfterTooManyCollisions proves the loop
// terminates. Retrying forever under a genuine stampede would livelock the
// worker that opened the incident.
func TestCreateIncidentWithNumber_GivesUpAfterTooManyCollisions(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	attempts := 0
	incident := models.NewIncident("org", "check", time.Now(), "stampede")

	err := db.CreateIncidentWithNumber(t.Context(), incident,
		func(_ context.Context, _ string) (int64, error) { return 1, nil },
		func(_ context.Context) error {
			attempts++

			return errSQLiteDuplicate
		})

	r.ErrorIs(err, db.ErrIncidentNumberExhausted)
	r.Equal(db.IncidentNumberAttempts, attempts)
}

// TestCreateIncidentWithNumber_KeepsAnExplicitNumber proves a pre-numbered
// incident (a fixture, a replay) is inserted untouched rather than renumbered.
func TestCreateIncidentWithNumber_KeepsAnExplicitNumber(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	incident := models.NewIncident("org", "check", time.Now(), "explicit")
	incident.Number = 42

	reads := 0

	err := db.CreateIncidentWithNumber(t.Context(), incident,
		func(_ context.Context, _ string) (int64, error) {
			reads++

			return 1, nil
		},
		func(_ context.Context) error { return nil })

	r.NoError(err)
	r.Zero(reads, "an explicit number must not trigger a MAX+1 read")
	r.EqualValues(42, incident.Number)
}
