package testruns

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAcquirePerOrgAndProcessCaps(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	limiter := NewLimiter(2, 1, 10)

	releaseA, err := limiter.Acquire("org-a")
	r.NoError(err)

	_, err = limiter.Acquire("org-a")
	r.ErrorIs(err, ErrOrgBusy, "one org holds at most maxPerOrg slots")

	releaseB, err := limiter.Acquire("org-b")
	r.NoError(err, "another org still runs")

	_, err = limiter.Acquire("org-c")
	r.ErrorIs(err, ErrProcessBusy, "the process cap holds across orgs")
	r.Equal(2, limiter.BusyOrgs())

	releaseA()
	releaseA() // idempotent
	releaseB()

	r.Zero(limiter.BusyOrgs())
	r.Empty(limiter.Slots())

	release, err := limiter.Acquire("org-a")
	r.NoError(err, "a released org runs again")
	release()
}

func TestAcquireWaitWaitsForTheSlot(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	limiter := NewLimiter(1, 1, 10)

	release, err := limiter.Acquire("org-a")
	r.NoError(err)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err = limiter.AcquireWait(ctx, "org-a")
	r.ErrorIs(err, ErrOrgBusy)
	r.ErrorIs(err, context.DeadlineExceeded)

	go func() {
		time.Sleep(100 * time.Millisecond)
		release()
	}()

	release2, err := limiter.AcquireWait(t.Context(), "org-a")
	r.NoError(err)
	release2()
}

func TestAllowCountsPerOrgPerMinute(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	limiter := NewLimiter(4, 1, 3)
	limiter.now = func() time.Time { return now }

	for range 3 {
		r.True(limiter.Allow("org-a"))
	}

	r.False(limiter.Allow("org-a"), "the 4th run inside the minute is refused")
	r.True(limiter.Allow("org-b"), "another org has its own allowance")

	now = now.Add(time.Minute)
	r.True(limiter.Allow("org-a"), "the allowance comes back the next minute")
}
