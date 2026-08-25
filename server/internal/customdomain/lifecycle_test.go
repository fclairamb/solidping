package customdomain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/customdomain"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// testNow is the fixed clock every transition is evaluated against, so the
// assertions on grace_since and the re-promotion timestamp are exact rather
// than approximate.
func testNow() time.Time {
	return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
}

func verifiedAt(offset time.Duration) *time.Time {
	at := testNow().Add(offset)

	return &at
}

// TestGraceKeepsServing is the core of requirement 2: crossing the grace
// threshold must NOT stop the page being served. VerifiedAt staying non-nil IS
// "keeps serving" — it is the gate every serving path reads.
func TestGraceKeepsServing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	state := customdomain.State{
		Lifecycle:  models.CustomDomainStateActive,
		VerifiedAt: verifiedAt(-24 * time.Hour),
	}

	for i := 1; i < customdomain.HardDemoteAfterFailures; i++ {
		out := customdomain.Next(state, customdomain.Observation{Now: testNow()})
		state = out.State

		r.NotNil(state.VerifiedAt, "failure %d must not stop the page being served", i)
		r.Equal(i, state.Failures)

		if i >= customdomain.GraceAfterFailures {
			r.Equal(models.CustomDomainStateGrace, state.Lifecycle, "failure %d", i)
			r.NotNil(state.GraceSince)
		} else {
			r.Equal(models.CustomDomainStateActive, state.Lifecycle, "failure %d", i)
		}
	}
}

// TestEnteredGraceFiresOnceOnTheTransition pins that the "entered grace" signal
// is edge-triggered, so a caller wiring a notification to it cannot spam.
func TestEnteredGraceFiresOnceOnTheTransition(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	state := customdomain.State{
		Lifecycle:  models.CustomDomainStateActive,
		VerifiedAt: verifiedAt(-24 * time.Hour),
		Failures:   customdomain.GraceAfterFailures - 1,
	}

	first := customdomain.Next(state, customdomain.Observation{Now: testNow()})
	r.True(first.EnteredGrace)
	r.False(first.HardDemoted)
	r.NotNil(first.GraceSince, "entering grace must stamp when degradation started")
	r.Equal(testNow(), *first.GraceSince)

	second := customdomain.Next(first.State, customdomain.Observation{Now: testNow()})
	r.False(second.EnteredGrace, "already in grace — the edge fires once")
	r.Equal(first.GraceSince, second.GraceSince, "grace_since must not be pushed forward")
}

// TestGraceSinceIsSetOnEntryAndClearedOnEveryExit covers the field surfaced to
// the dashboard as customDomainDegradedSince. It is the operator's answer to
// "how long has this been broken", so a stale value left behind after recovery
// would be worse than no value at all — it would report a healthy domain as
// degrading since some date in the past.
func TestGraceSinceIsSetOnEntryAndClearedOnEveryExit(t *testing.T) {
	t.Parallel()

	graced := func() customdomain.State {
		t.Helper()

		state := customdomain.State{
			Lifecycle:  models.CustomDomainStateActive,
			VerifiedAt: verifiedAt(-24 * time.Hour),
			Failures:   customdomain.GraceAfterFailures - 1,
		}
		out := customdomain.Next(state, customdomain.Observation{Now: testNow()})
		require.NotNil(t, out.GraceSince)

		return out.State
	}

	t.Run("healthy states carry no degraded-since", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		state := customdomain.State{
			Lifecycle:  models.CustomDomainStateActive,
			VerifiedAt: verifiedAt(-24 * time.Hour),
		}

		out := customdomain.Next(state, customdomain.Observation{OK: true, Now: testNow()})
		r.Nil(out.GraceSince)

		belowThreshold := customdomain.Next(state, customdomain.Observation{Now: testNow()})
		r.Nil(belowThreshold.GraceSince, "a single failure is not yet degradation")
	})

	t.Run("recovering from grace clears it", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		out := customdomain.Next(graced(), customdomain.Observation{OK: true, Now: testNow()})
		r.True(out.Recovered)
		r.Nil(out.GraceSince, "a recovered domain must not still report a degraded-since")
	})

	t.Run("hard demotion clears it too", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		state := graced()
		state.Failures = customdomain.HardDemoteAfterFailures - 1

		out := customdomain.Next(state, customdomain.Observation{Now: testNow()})
		r.True(out.HardDemoted)
		r.Nil(out.GraceSince, "the domain is dark now, not degrading")
	})
}

// TestHardDemotionOnlyAtTheFarThreshold pins that going dark takes days of
// uninterrupted failure, not one sweep past the grace threshold.
func TestHardDemotionOnlyAtTheFarThreshold(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	state := customdomain.State{
		Lifecycle:  models.CustomDomainStateGrace,
		VerifiedAt: verifiedAt(-72 * time.Hour),
		GraceSince: verifiedAt(-48 * time.Hour),
		Failures:   customdomain.HardDemoteAfterFailures - 2,
	}

	notYet := customdomain.Next(state, customdomain.Observation{Now: testNow()})
	r.False(notYet.HardDemoted)
	r.NotNil(notYet.VerifiedAt, "one short of the threshold still serves")

	dark := customdomain.Next(notYet.State, customdomain.Observation{Now: testNow()})
	r.True(dark.HardDemoted)
	r.Nil(dark.VerifiedAt)
	r.Equal(models.CustomDomainStateDemoted, dark.Lifecycle)
	r.Nil(dark.GraceSince)
}

// TestSuccessInGraceRecoversInvisibly is the common case the whole state
// machine exists for: a blip ends and nobody outside ever saw it.
func TestSuccessInGraceRecoversInvisibly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	state := customdomain.State{
		Lifecycle:  models.CustomDomainStateGrace,
		VerifiedAt: verifiedAt(-72 * time.Hour),
		GraceSince: verifiedAt(-24 * time.Hour),
		Failures:   customdomain.GraceAfterFailures + 1,
	}

	out := customdomain.Next(state, customdomain.Observation{OK: true, Now: testNow()})
	r.True(out.Recovered)
	r.Equal(models.CustomDomainStateActive, out.Lifecycle)
	r.Zero(out.Failures)
	r.Nil(out.GraceSince)
	r.Equal(state.VerifiedAt, out.VerifiedAt, "the page never stopped serving, so nothing is re-stamped")
}

// TestDemotedDomainRepromotesAfterConsecutiveSuccesses is requirement 1: a
// demoted domain recovers on its own, and needs more than one success to do it.
func TestDemotedDomainRepromotesAfterConsecutiveSuccesses(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	state := customdomain.State{
		Lifecycle: models.CustomDomainStateDemoted,
		Failures:  customdomain.HardDemoteAfterFailures,
	}

	for i := 1; i < customdomain.RepromoteSuccesses; i++ {
		out := customdomain.Next(state, customdomain.Observation{OK: true, CertValid: true, Now: testNow()})
		state = out.State

		r.False(out.Repromoted, "success %d must not be enough on its own", i)
		r.Nil(state.VerifiedAt)
		r.Equal(i, state.Successes)
	}

	final := customdomain.Next(state, customdomain.Observation{OK: true, CertValid: true, Now: testNow()})
	r.True(final.Repromoted)
	r.NotNil(final.VerifiedAt)
	r.Equal(testNow(), *final.VerifiedAt)
	r.Equal(models.CustomDomainStateActive, final.Lifecycle)
	r.Zero(final.Successes)
}

// TestRepromotionRequiresAValidCertificate is the negative control for the
// second half of the re-promotion gate. Without it the test above would pass
// for the wrong reason — "three successes" alone, with the certificate
// condition deleted.
func TestRepromotionRequiresAValidCertificate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	state := customdomain.State{
		Lifecycle: models.CustomDomainStateDemoted,
		Successes: customdomain.RepromoteSuccesses,
	}

	out := customdomain.Next(state, customdomain.Observation{OK: true, CertValid: false, Now: testNow()})
	r.False(out.Repromoted, "no held certificate ⇒ no automatic re-promotion")
	r.Nil(out.VerifiedAt)
	r.Equal(models.CustomDomainStateDemoted, out.Lifecycle)
}

// TestPendingDomainIsNeverAutoPromoted pins the boundary of requirement 1: only
// a domain that WAS ours may earn its way back. A hostname that has never
// verified still needs an operator, however well it resolves.
func TestPendingDomainIsNeverAutoPromoted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	state := customdomain.State{Lifecycle: models.CustomDomainStatePending}

	for range customdomain.RepromoteSuccesses + 3 {
		out := customdomain.Next(state, customdomain.Observation{OK: true, CertValid: true, Now: testNow()})
		state = out.State

		r.False(out.Repromoted)
		r.Nil(state.VerifiedAt)
		r.Equal(models.CustomDomainStatePending, state.Lifecycle)
	}
}

// TestNormalizeDerivesDemotedForLegacyRows is what makes the fix reach the rows
// that are already broken: a page written before the state column existed, and
// demoted by the old one-way sweep, must be recognized as `demoted` — not
// `pending` — or it could never recover.
func TestNormalizeDerivesDemotedForLegacyRows(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	legacy := customdomain.Normalize(customdomain.State{}, true, true)
	r.Equal(models.CustomDomainStateDemoted, legacy.Lifecycle)

	neverChecked := customdomain.Normalize(customdomain.State{}, true, false)
	r.Equal(models.CustomDomainStatePending, neverChecked.Lifecycle)

	serving := customdomain.Normalize(
		customdomain.State{VerifiedAt: verifiedAt(-time.Hour)}, true, true)
	r.Equal(models.CustomDomainStateActive, serving.Lifecycle)

	noDomain := customdomain.Normalize(customdomain.State{}, false, false)
	r.Equal(models.CustomDomainStateNone, noDomain.Lifecycle)

	explicit := customdomain.Normalize(
		customdomain.State{Lifecycle: models.CustomDomainStateGrace}, true, true)
	r.Equal(models.CustomDomainStateGrace, explicit.Lifecycle, "an explicit state is never overridden")
}

// TestCountersAreMutuallyExclusive pins that the two runs never both hold a
// value — a success run and a failure run are the same fact seen from two
// sides, and letting both be non-zero is how one counter ended up doing two
// jobs in the first place.
func TestCountersAreMutuallyExclusive(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	state := customdomain.State{
		Lifecycle: models.CustomDomainStateDemoted,
		Failures:  7,
	}

	out := customdomain.Next(state, customdomain.Observation{OK: true, Now: testNow()})
	r.Zero(out.Failures)
	r.Equal(1, out.Successes)

	back := customdomain.Next(out.State, customdomain.Observation{Now: testNow()})
	r.Zero(back.Successes)
	r.Equal(1, back.Failures)
}
