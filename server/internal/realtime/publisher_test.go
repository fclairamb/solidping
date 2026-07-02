package realtime

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/notifier"
)

// hintCapture drains a notifier Listen channel into a timestamped log so
// tests can assert on publish counts and timing without racing the bus.
type hintCapture struct {
	mu       sync.Mutex
	payloads []string
	times    []time.Time
	done     chan struct{}
}

func newHintCapture(t *testing.T, bus notifier.EventNotifier) *hintCapture {
	t.Helper()

	c := &hintCapture{done: make(chan struct{})}
	ch := bus.Listen(ChannelOrgEvents)

	go func() {
		defer close(c.done)
		for payload := range ch {
			c.mu.Lock()
			c.payloads = append(c.payloads, payload)
			c.times = append(c.times, time.Now())
			c.mu.Unlock()
		}
	}()

	return c
}

func (c *hintCapture) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]string, len(c.payloads))
	copy(out, c.payloads)

	return out
}

func (c *hintCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.payloads)
}

func TestPublisher_FirstHintIsImmediate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	capture := newHintCapture(t, bus)

	pub := NewPublisher(t.Context(), bus, time.Second, nil)
	defer pub.Close()

	start := time.Now()
	pub.Publish(context.Background(), "org-1", "check-1", KindResults)

	r.Eventually(func() bool { return capture.count() == 1 }, time.Second, time.Millisecond,
		"leading-edge hint must publish immediately, not wait for the flush ticker")
	r.Less(time.Since(start), 500*time.Millisecond)

	hint, err := DecodeHint(capture.snapshot()[0])
	r.NoError(err)
	r.Equal("org-1", hint.Org)
	r.Equal([]string{"results"}, hint.Kinds)
	r.Equal([]string{"check-1"}, hint.CheckUids)
}

// TestPublisher_BurstCoalesces drives a continuous burst of hints for one org
// and asserts the coalescer contract: the first hint is immediate and
// subsequent publishes are bounded by ~1 per flush interval per instance.
func TestPublisher_BurstCoalesces(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const flushInterval = 100 * time.Millisecond
	const burstDuration = 550 * time.Millisecond

	bus := notifier.NewLocalEventNotifier()
	capture := newHintCapture(t, bus)

	pub := NewPublisher(t.Context(), bus, flushInterval, nil)

	start := time.Now()
	for time.Since(start) < burstDuration {
		pub.Publish(context.Background(), "org-burst", "check-a", KindResults, KindJobs)
		time.Sleep(time.Millisecond)
	}
	pub.Close() // flushes the trailing dirty set

	r.NoError(bus.Close())
	<-capture.done

	count := capture.count()
	r.GreaterOrEqual(count, 2, "expected the leading edge plus at least one flush")

	// ≤ 1 publish per interval: leading edge + one per elapsed window, plus
	// the final Close flush.
	maxExpected := int(burstDuration/flushInterval) + 2
	r.LessOrEqual(count, maxExpected,
		"burst must coalesce to at most ~1 publish per flush interval")

	// Every publish carries the merged kind set.
	for _, payload := range capture.snapshot() {
		hint, err := DecodeHint(payload)
		r.NoError(err)
		r.Equal("org-burst", hint.Org)
		r.Equal([]string{"jobs", "results"}, hint.Kinds)
		r.Equal([]string{"check-a"}, hint.CheckUids)
	}
}

func TestPublisher_PublishImmediateBypassesWindowAndMergesPending(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	capture := newHintCapture(t, bus)

	pub := NewPublisher(t.Context(), bus, time.Minute, nil) // window long enough to never tick
	defer pub.Close()

	ctx := context.Background()
	pub.Publish(ctx, "org-imm", "check-1", KindResults) // leading edge, published
	pub.Publish(ctx, "org-imm", "check-2", KindJobs)    // coalesced into pending
	pub.PublishImmediate(ctx, "org-imm", "check-3", KindIncidents, KindEvents, KindChecks)

	r.Eventually(func() bool { return capture.count() == 2 }, time.Second, time.Millisecond)

	second, err := DecodeHint(capture.snapshot()[1])
	r.NoError(err)
	r.Equal("org-imm", second.Org)
	// Pending "jobs"/check-2 folded into the immediate publish.
	r.Equal([]string{"checks", "events", "incidents", "jobs"}, second.Kinds)
	r.Equal([]string{"check-2", "check-3"}, second.CheckUids)
}

func TestPublisher_OrgsAreIndependent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	capture := newHintCapture(t, bus)

	pub := NewPublisher(t.Context(), bus, time.Minute, nil)
	defer pub.Close()

	ctx := context.Background()
	pub.Publish(ctx, "org-a", "check-1", KindResults)
	pub.Publish(ctx, "org-b", "check-2", KindResults)

	r.Eventually(func() bool { return capture.count() == 2 }, time.Second, time.Millisecond,
		"each org gets its own leading edge")
}

func TestPublisher_NilAndEmptyAreSafe(t *testing.T) {
	t.Parallel()

	var pub *Publisher
	pub.Publish(context.Background(), "org", "check", KindResults)
	pub.PublishImmediate(context.Background(), "org", "check", KindResults)
	pub.Close()

	wired := NewPublisher(t.Context(), notifier.NewLocalEventNotifier(), time.Second, nil)
	defer wired.Close()
	wired.Publish(context.Background(), "", "check", KindResults)
	wired.Publish(context.Background(), "org", "check")
}

// TestPublisher_OrgLevelKindsCarryNoCheckUids proves events/jobs (checkUID
// "") never populate CheckUids, even when merged with check-attributable
// kinds in the same window.
func TestPublisher_OrgLevelKindsCarryNoCheckUids(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	capture := newHintCapture(t, bus)

	pub := NewPublisher(t.Context(), bus, time.Minute, nil)
	defer pub.Close()

	pub.Publish(context.Background(), "org-1", "", KindJobs)

	r.Eventually(func() bool { return capture.count() == 1 }, time.Second, time.Millisecond)

	hint, err := DecodeHint(capture.snapshot()[0])
	r.NoError(err)
	r.Equal([]string{"jobs"}, hint.Kinds)
	r.Empty(hint.CheckUids)
}

// TestPublisher_StormCollapsesCheckUidsToWildcard proves that once a flush
// window accumulates more than CollapseCheckUids distinct check uids for an
// org, the published hint carries the ["*"] wildcard instead of the full
// list — bounding the NOTIFY payload regardless of outage size.
func TestPublisher_StormCollapsesCheckUidsToWildcard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	capture := newHintCapture(t, bus)

	pub := NewPublisher(t.Context(), bus, time.Minute, nil) // never ticks; stays in one pending window
	defer pub.Close()

	ctx := context.Background()
	pub.Publish(ctx, "org-storm", "check-0", KindResults) // leading edge, consumes uid 0
	for i := 1; i <= CollapseCheckUids+10; i++ {
		pub.Publish(ctx, "org-storm", fmt.Sprintf("check-%d", i), KindResults)
	}
	pub.PublishImmediate(ctx, "org-storm", "check-final", KindResults)

	r.Eventually(func() bool { return capture.count() == 2 }, time.Second, time.Millisecond)

	final, err := DecodeHint(capture.snapshot()[1])
	r.NoError(err)
	r.Equal([]string{"*"}, final.CheckUids, "storm collapse must replace the check-uid list with the wildcard")
	r.True(IsWildcardCheckUids(final.CheckUids))
}
