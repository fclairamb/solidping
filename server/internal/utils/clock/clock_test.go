package clock_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

func TestFakeAdvanceNow(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start)

	r.Equal(start, clk.Now())

	clk.Advance(10 * time.Second)
	r.Equal(start.Add(10*time.Second), clk.Now())

	clk.Advance(5 * time.Minute)
	r.Equal(start.Add(10*time.Second+5*time.Minute), clk.Now())
}

func TestFakeSince(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start)

	past := start.Add(-30 * time.Second)
	r.Equal(30*time.Second, clk.Since(past))

	clk.Advance(15 * time.Second)
	r.Equal(45*time.Second, clk.Since(past))
}

func TestFakeAfterFires(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start)

	ch := clk.After(10 * time.Second)

	// Should not fire yet.
	select {
	case <-ch:
		r.Fail("After fired before Advance")
	default:
	}

	// Advance past the deadline.
	clk.Advance(11 * time.Second)

	select {
	case fired := <-ch:
		r.False(fired.IsZero(), "fired time should be non-zero")
	case <-time.After(1 * time.Second):
		r.Fail("After did not fire after Advance")
	}
}

func TestRealImplementsClock(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var clk clock.Clock = clock.Real{}
	before := time.Now()
	now := clk.Now()
	after := time.Now()

	r.False(now.Before(before))
	r.False(now.After(after))
}
