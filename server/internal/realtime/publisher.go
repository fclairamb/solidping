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

// pendingOrg is one org's coalescing window state: the dirty kinds, the check
// uids the check-attributable kinds apply to, and whether that set has
// already collapsed to the ["*"] wildcard (once collapsed, further uids are
// no-ops — the window already means "every check").
type pendingOrg struct {
	kinds     map[Kind]struct{}
	checkUids map[string]struct{}
	collapsed bool
}

func newPendingOrg() *pendingOrg {
	return &pendingOrg{kinds: make(map[Kind]struct{}), checkUids: make(map[string]struct{})}
}

// merge folds kinds and a check uid ("" for org-level kinds) into the pending
// state, collapsing checkUids to the wildcard once CollapseCheckUids is
// exceeded.
func (p *pendingOrg) merge(kinds []Kind, checkUID string) {
	for _, k := range kinds {
		p.kinds[k] = struct{}{}
	}
	if p.collapsed || checkUID == "" {
		return
	}
	p.checkUids[checkUID] = struct{}{}
	if len(p.checkUids) > CollapseCheckUids {
		p.collapsed = true
		p.checkUids = nil
	}
}

// Publisher publishes org hint events to the notifier bus through a
// leading-edge, per-org, in-memory coalescer: the first hint for an org goes
// out immediately (incidents must feel instant); hints arriving within the
// flush window merge into a per-org dirty kind-set (plus check-uid
// attribution) flushed by a ticker. This bounds bus traffic to
// ≤1 publish/org/interval/instance for coalesced kinds regardless of result
// volume — NOTIFY serializes on a global queue lock at commit, so the result
// write path must never publish per result.
//
// All methods are nil-receiver safe: an unwired *Publisher (tests, disabled
// feature, auxiliary tooling) silently no-ops.
type Publisher struct {
	notifier      notifier.EventNotifier
	flushInterval time.Duration
	logger        *slog.Logger

	mu          sync.Mutex
	pending     map[string]*pendingOrg // org uid -> dirty state awaiting flush
	lastPublish map[string]time.Time   // org uid -> last bus publish

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
		pending:       make(map[string]*pendingOrg),
		lastPublish:   make(map[string]time.Time),
		done:          make(chan struct{}),
	}

	pub.wg.Add(1)
	go pub.flushLoop(ctx)

	return pub
}

// Publish records a hint for the org, coalescing within the flush window.
// checkUID attributes the hint to one check (results/checks/incidents); pass
// "" for org-level kinds (events, jobs). Leading edge: if the org has not
// published recently, the hint goes out immediately; otherwise the kinds (and
// check uid) join the org's pending dirty set and ride the next ticker flush.
func (p *Publisher) Publish(ctx context.Context, orgUID, checkUID string, kinds ...Kind) {
	if p == nil || orgUID == "" || len(kinds) == 0 {
		return
	}

	p.mu.Lock()
	if pending, ok := p.pending[orgUID]; ok {
		// A flush is already scheduled for this org — merge and wait.
		pending.merge(kinds, checkUID)
		p.mu.Unlock()
		prommetrics.RealtimeHintsCoalesced.Inc()

		return
	}

	if time.Since(p.lastPublish[orgUID]) < p.flushInterval {
		// Inside the window: open a dirty set for the next flush.
		pending := newPendingOrg()
		pending.merge(kinds, checkUID)
		p.pending[orgUID] = pending
		p.mu.Unlock()
		prommetrics.RealtimeHintsCoalesced.Inc()

		return
	}

	// Leading edge — publish now.
	p.lastPublish[orgUID] = time.Now()
	p.mu.Unlock()

	leading := newPendingOrg()
	leading.merge(kinds, checkUID)
	p.publish(ctx, orgUID, leading)
}

// PublishImmediate publishes now, bypassing the coalescing window. Any kinds
// and check uids already pending for the org are folded into this publish so
// ordering and dedup are preserved. Used for rare, latency-critical hints
// (status transitions, incident lifecycle).
func (p *Publisher) PublishImmediate(ctx context.Context, orgUID, checkUID string, kinds ...Kind) {
	if p == nil || orgUID == "" || len(kinds) == 0 {
		return
	}

	p.mu.Lock()
	merged, ok := p.pending[orgUID]
	if !ok {
		merged = newPendingOrg()
	}
	merged.merge(kinds, checkUID)
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
	p.pending = make(map[string]*pendingOrg)

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

	for orgUID, pending := range batch {
		p.publish(ctx, orgUID, pending)
	}
}

// publish encodes and sends one hint on the bus. Best-effort: failures are
// logged, never propagated — a missed hint is recovered by the fallback poll.
func (p *Publisher) publish(ctx context.Context, orgUID string, pending *pendingOrg) {
	payload, err := encodePendingHint(orgUID, pending)
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
