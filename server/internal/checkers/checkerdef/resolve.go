package checkerdef

import (
	"context"
	"errors"
	"net"
	"time"
)

const (
	// resolveAttemptTimeout caps one lookup attempt. The stdlib resolver waits
	// up to resolv.conf's per-exchange timeout (default 5s) on a lost UDP
	// packet — as long as, or longer than, a fast check's whole budget (the
	// scheduler's cost-aware timeout floor is 5s), so a single lost packet
	// used to fail the check outright. Cutting the attempt short and
	// re-issuing the lookup sends fresh queries from a new source port (a new
	// conntrack flow, often landing on a different DNS endpoint), which beats
	// waiting out a packet that is never coming.
	resolveAttemptTimeout = 1500 * time.Millisecond

	// resolveMaxAttempts bounds the retry loop; with a parent deadline the
	// context stops the loop first (each attempt is clamped to the parent).
	resolveMaxAttempts = 3
)

// LookupIPAddr resolves host like net.Resolver.LookupIPAddr, but retries
// transient failures (timeouts, temporary server errors) with short attempts
// instead of letting one lost DNS packet consume the caller's entire budget.
// Non-transient failures (NXDOMAIN, cancellation, ...) return immediately.
// IP literals resolve without any DNS traffic, exactly like the stdlib.
func LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return lookupIPAddr(ctx, host, net.DefaultResolver.LookupIPAddr)
}

// lookupIPAddr is LookupIPAddr with the actual lookup injectable for tests.
func lookupIPAddr(
	ctx context.Context, host string,
	lookup func(context.Context, string) ([]net.IPAddr, error),
) ([]net.IPAddr, error) {
	var lastErr error

	for attempt := 0; attempt < resolveMaxAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, resolveAttemptTimeout)
		addrs, err := lookup(attemptCtx, host)

		cancel()

		if err == nil {
			return addrs, nil
		}

		lastErr = err

		if !isTransientResolveError(err) || ctx.Err() != nil {
			break
		}
	}

	return nil, lastErr
}

// isTransientResolveError reports whether a lookup error is worth retrying:
// timeouts and temporary server failures are; NXDOMAIN and cancellations are
// definitive answers and are not.
func isTransientResolveError(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return !dnsErr.IsNotFound && (dnsErr.IsTimeout || dnsErr.IsTemporary)
	}

	return false
}
