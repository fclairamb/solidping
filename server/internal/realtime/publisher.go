package realtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// DefaultFlushInterval is the default coalescing window for noisy kinds.
const DefaultFlushInterval = time.Second

// Publisher publishes org hint events to the notifier bus through a
// leading-edge, per-org, in-memory coalescer: the first hint for an org goes
// out immediately (incidents must feel instant); hints arriving within the
// flush window merge into a per-org dirty kind-set flushed by a ticker. This
// bounds bus traffic to ≤1 publish/org/interval/instance for coalesced kinds
// regardless of result volume — NOTIFY serializes on a global queue lock at
// commit, so the result write path must never publish per result.
//
// All methods are nil-receiver safe: an unwired *Publisher (tests, disabled
// feature, auxiliary tooling) silently no-ops.
type Publisher struct {
	notifier      notifier.EventNotifier
	flushInterval time.Duration
	logger        *slog.Logger

	mu          sync.Mutex
	pending     map[string]map[Kind]struct{} // org uid -> dirty kinds awaiting flush
	lastPublish map[string]time.Time         // org uid -> last bus publish

	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// NewPublisher creates a publisher on the given bus and starts its flush loop.
// ctx is the process lifetime context used by background flushes (Notify is a
// DB exec on Postgres). A non-positive flushInterval falls back to
// DefaultFlushInterval.
func NewPublisher(
	ctx context.Context, bus notifier.EventNotifier, flushInterval time.Duration, logger *slog.Logger,
) *Publisher {
	if logger == nil {
		logger = slog.Default()
	}
	if flushInterval <= 0 {
		flushInterval = DefaultFlushInterval
	}

	pub := &Publisher{
		notifier:      bus,
		flushInterval: flushInterval,
		logger:        logger.With("component", "realtime_publisher"),
		pending:       make(map[string]map[Kind]struct{}),
		lastPublish:   make(map[string]time.Time),
		done:          make(chan struct{}),
	}

	pub.wg.Add(1)
	go pub.flushLoop(ctx)

	return pub
}

// Publish records a hint for the org, coalescing within the flush window.
// Leading edge: if the org has not published recently, the hint goes out
// immediately; otherwise the kinds join the org's pending dirty set and ride
// the next ticker flush.
func (p *Publisher) Publish(ctx context.Context, orgUID string, kinds ...Kind) {
	if p == nil || orgUID == "" || len(kinds) == 0 {
		return
	}

	p.mu.Lock()
	if pending, ok := p.pending[orgUID]; ok {
		// A flush is already scheduled for this org — merge and wait.
		for _, k := range kinds {
			pending[k] = struct{}{}
		}
		p.mu.Unlock()
		prommetrics.RealtimeHintsCoalesced.Inc()

		return
	}

	if time.Since(p.lastPublish[orgUID]) < p.flushInterval {
		// Inside the window: open a dirty set for the next flush.
		p.pending[orgUID] = kindSet(kinds)
		p.mu.Unlock()
		prommetrics.RealtimeHintsCoalesced.Inc()

		return
	}

	// Leading edge — publish now.
	p.lastPublish[orgUID] = time.Now()
	p.mu.Unlock()

	p.publish(ctx, orgUID, kindSet(kinds))
}

// PublishImmediate publishes now, bypassing the coalescing window. Any kinds
// already pending for the org are folded into this publish so ordering and
// dedup are preserved. Used for rare, latency-critical hints (status
// transitions, incident lifecycle).
func (p *Publisher) PublishImmediate(ctx context.Context, orgUID string, kinds ...Kind) {
	if p == nil || orgUID == "" || len(kinds) == 0 {
		return
	}

	merged := kindSet(kinds)

	p.mu.Lock()
	for k := range p.pending[orgUID] {
		merged[k] = struct{}{}
	}
	delete(p.pending, orgUID)
	p.lastPublish[orgUID] = time.Now()
	p.mu.Unlock()

	p.publish(ctx, orgUID, merged)
}

// Close stops the flush loop after emitting any still-pending hints. The
// final flush runs on a fresh background context: Close is called during
// shutdown when the caller's context is typically already canceled.
func (p *Publisher) Close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		close(p.done)
		p.wg.Wait()
		p.flush(context.Background())
	})
}

// flushLoop emits pending dirty sets every flush interval.
func (p *Publisher) flushLoop(ctx context.Context) {
	defer p.wg.Done()

	ticker := time.NewTicker(p.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.flush(ctx)
		case <-p.done:
			return
		}
	}
}

// flush publishes every pending per-org dirty set and prunes stale
// last-publish entries so the map stays bounded by recently-active orgs.
func (p *Publisher) flush(ctx context.Context) {
	p.mu.Lock()
	batch := p.pending
	p.pending = make(map[string]map[Kind]struct{})

	now := time.Now()
	for orgUID := range batch {
		p.lastPublish[orgUID] = now
	}
	// Prune orgs idle for several windows; their next hint is leading-edge
	// again anyway.
	const pruneWindows = 10
	horizon := now.Add(-pruneWindows * p.flushInterval)
	for orgUID, last := range p.lastPublish {
		if _, isPending := batch[orgUID]; !isPending && last.Before(horizon) {
			delete(p.lastPublish, orgUID)
		}
	}
	p.mu.Unlock()

	for orgUID, kinds := range batch {
		p.publish(ctx, orgUID, kinds)
	}
}

// publish encodes and sends one hint on the bus. Best-effort: failures are
// logged, never propagated — a missed hint is recovered by the fallback poll.
func (p *Publisher) publish(ctx context.Context, orgUID string, kinds map[Kind]struct{}) {
	payload, err := EncodeHint(orgUID, kinds)
	if err != nil {
		p.logger.WarnContext(ctx, "failed to encode realtime hint", "org", orgUID, "error", err)

		return
	}

	if err := p.notifier.Notify(ctx, ChannelOrgEvents, payload); err != nil {
		p.logger.DebugContext(ctx, "failed to publish realtime hint", "org", orgUID, "error", err)

		return
	}

	prommetrics.RealtimeHintsPublished.Inc()
}
