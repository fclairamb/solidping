package egress

import (
	"cmp"
	"context"
	"errors"
	"net"
	"time"
)

// defaultFallbackDelay is net.Dialer's own default (RFC 8305 recommends 250 ms).
const defaultFallbackDelay = 300 * time.Millisecond

// dialHappyEyeballs dials the pinned ips the way net.Dialer dials a name: the
// addresses of the first answer's family in order, and, unless the dialer's
// FallbackDelay is negative, the other family in order started FallbackDelay
// later, racing. The first connection wins and the other race is canceled.
//
// Without it the guard dialed the pinned IPs one after the other, each with the
// full dial timeout, so under a probe budget an IPv6 path losing SYNs made a
// dual-stack target look down although IPv4 answered fine.
func dialHappyEyeballs(
	ctx context.Context, dialer *net.Dialer, network, port string, ips []net.IP,
) (net.Conn, error) {
	primaries, fallbacks := splitByFamily(ips)
	if len(fallbacks) == 0 || dialer.FallbackDelay < 0 {
		return dialSerial(ctx, dialer, network, port, ips)
	}

	delay := dialer.FallbackDelay
	if delay == 0 {
		delay = defaultFallbackDelay
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan dialResult, 2) // buffered: a late loser never blocks

	start := func(list []net.IP, primary bool) {
		go func() {
			conn, err := dialSerial(raceCtx, dialer, network, port, list)
			results <- dialResult{conn: conn, err: err, primary: primary}
		}()
	}

	start(primaries, true)

	fallbackTimer := time.NewTimer(delay)
	defer fallbackTimer.Stop()

	pending, fallbackStarted := 1, false

	startFallback := func() {
		if !fallbackStarted {
			fallbackStarted = true
			pending++

			start(fallbacks, false)
		}
	}

	var primaryErr, fallbackErr error

	for {
		select {
		case <-fallbackTimer.C:
			startFallback()
		case res := <-results:
			pending--

			if res.err == nil || errors.Is(res.err, ErrDenied) {
				cancel()
				closeLosers(results, pending)

				return res.conn, res.err
			}

			if res.primary {
				primaryErr = res.err
			} else {
				fallbackErr = res.err
			}

			// The primary family failed before the delay: try the other now.
			startFallback()

			if pending == 0 {
				return nil, cmp.Or(primaryErr, fallbackErr) // like net.Dialer: the primary's error
			}
		}
	}
}

// dialResult is one family's outcome in a Happy Eyeballs race.
type dialResult struct {
	conn    net.Conn
	err     error
	primary bool
}

// closeLosers closes, in the background, the connections the still-running
// racers may yet return after the race was decided.
func closeLosers(results <-chan dialResult, pending int) {
	if pending == 0 {
		return
	}

	go func() {
		for range pending {
			if res := <-results; res.conn != nil {
				_ = res.conn.Close()
			}
		}
	}()
}

// dialSerial dials ips in order until one connects, stopping on a refusal or
// when ctx is done.
func dialSerial(
	ctx context.Context, dialer *net.Dialer, network, port string, ips []net.IP,
) (net.Conn, error) {
	var lastErr error

	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}

		lastErr = err

		if errors.Is(err, ErrDenied) || ctx.Err() != nil {
			break
		}
	}

	return nil, lastErr
}

// splitByFamily splits ips into those of the first address's family and the
// rest, each keeping the resolver order.
func splitByFamily(ips []net.IP) ([]net.IP, []net.IP) {
	var primaries, fallbacks []net.IP

	firstIs4 := ips[0].To4() != nil

	for _, ip := range ips {
		if (ip.To4() != nil) == firstIs4 {
			primaries = append(primaries, ip)
		} else {
			fallbacks = append(fallbacks, ip)
		}
	}

	return primaries, fallbacks
}
