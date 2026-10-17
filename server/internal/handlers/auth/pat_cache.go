package auth

import (
	"context"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/notifier"
)

// patCacheTTL bounds how long a validated PAT is served from memory. Since
// revocations are broadcast (EventPATRevoked), it only bounds the rare
// dropped notification; a miss costs four indexed reads (token, user, org,
// membership).
var patCacheTTL = 5 * time.Minute //nolint:gochecknoglobals // overridable in tests

// EventPATRevoked is broadcast with the revoked PAT's token hash as payload, so
// every replica evicts it from its cache. It must not contain '_': the
// Postgres notifier maps '.' to '_' in channel names and back.
const EventPATRevoked = "auth.patrevoked"

// cachedPAT returns a copy of the cached claims for the hash, if any and not
// expired. Each caller gets its own copy: the middleware may mutate the
// claims it is handed (applyDemoClaim), and concurrent requests must not share
// one struct.
func (s *Service) cachedPAT(cacheKey string) (*Claims, bool) {
	s.cacheMux.RLock()
	defer s.cacheMux.RUnlock()

	cached, exists := s.patCache[cacheKey]
	if !exists || !time.Now().Before(cached.expiresAt) {
		return nil, false
	}

	claims := *cached.claims

	return &claims, true
}

// storePAT caches the claims, unless an eviction happened since gen was read
// (the lookup may then have raced a revocation). The entry never outlives the
// token's own expiry.
func (s *Service) storePAT(cacheKey string, claims *Claims, tokenExpiresAt *time.Time, gen uint64) {
	expiresAt := time.Now().Add(patCacheTTL)
	if tokenExpiresAt != nil && tokenExpiresAt.Before(expiresAt) {
		expiresAt = *tokenExpiresAt
	}

	stored := *claims

	s.cacheMux.Lock()
	if s.patCacheGen.Load() == gen {
		s.patCache[cacheKey] = &cachedPATClaims{claims: &stored, expiresAt: expiresAt}
	}
	s.cacheMux.Unlock()

	// Periodically cleanup expired cache entries (1% chance on each call)
	if time.Now().UnixNano()%100 == 0 {
		go s.cleanupExpiredPATCache()
	}
}

// evictPAT drops one hash from the cache and bumps the generation, so a
// lookup already past its database read cannot re-insert it.
func (s *Service) evictPAT(tokenHash string) {
	s.cacheMux.Lock()
	delete(s.patCache, tokenHash)
	s.patCacheGen.Add(1)
	s.cacheMux.Unlock()
}

// clearPATCache drops every entry and bumps the generation. Used after a
// notifier reconnect, when revocations may have been missed.
func (s *Service) clearPATCache() {
	s.cacheMux.Lock()
	clear(s.patCache)
	s.patCacheGen.Add(1)
	s.cacheMux.Unlock()
}

func (s *Service) cleanupExpiredPATCache() {
	s.cacheMux.Lock()
	defer s.cacheMux.Unlock()

	now := time.Now()
	for token, cached := range s.patCache {
		if now.After(cached.expiresAt) {
			delete(s.patCache, token)
		}
	}
}

// broadcastPATRevoked tells the other replicas to evict the hash. Best-effort:
// a lost notification is bounded by patCacheTTL.
func (s *Service) broadcastPATRevoked(ctx context.Context, tokenHash string) {
	if s.eventNotifier == nil {
		return
	}

	if err := s.eventNotifier.Notify(ctx, EventPATRevoked, tokenHash); err != nil {
		slog.WarnContext(ctx, "Failed to broadcast PAT revocation", "error", err)
	}
}

// SetEventNotifier wires the cross-replica bus, late like SetSupportInbox.
// It starts one goroutine that evicts every revoked hash broadcast on the bus
// and clears the whole cache when the transport reconnects. The goroutine
// stops on ctx.Done() or when the notifier closes its channel. Call it once,
// at boot, before the service serves requests. A nil notifier keeps eviction
// local.
func (s *Service) SetEventNotifier(ctx context.Context, n notifier.EventNotifier) {
	s.eventNotifier = n
	if n == nil {
		return
	}

	events := n.Listen(EventPATRevoked)

	var reconnects <-chan struct{}
	if rn, ok := n.(notifier.ReconnectNotifier); ok {
		reconnects = rn.ReconnectEvents()
	}

	go s.runPATRevocationListener(ctx, n, events, reconnects)
}

func (s *Service) runPATRevocationListener(
	ctx context.Context, n notifier.EventNotifier, events <-chan string, reconnects <-chan struct{},
) {
	defer n.Unlisten(EventPATRevoked, events)

	for {
		select {
		case tokenHash, ok := <-events:
			if !ok {
				return
			}

			s.evictPAT(tokenHash)
		case _, ok := <-reconnects:
			if !ok {
				reconnects = nil

				continue
			}

			s.clearPATCache()
		case <-ctx.Done():
			return
		}
	}
}
