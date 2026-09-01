package checkgrpc

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Phase names recorded on a failing result under output["phase"]. They answer
// "how far did the probe get?", which is the whole point of instrumenting the
// connection instead of letting the first RPC absorb it: a DNS failure, a
// refused connection, a TLS handshake error and an unhealthy service used to
// be one indistinguishable "health check failed: rpc error: …".
const (
	phaseDNS     = "dns"
	phaseConnect = "connect"
	phaseTLS     = "tls-handshake"
	phaseRPC     = "rpc"
)

// phaseTimer records what each connection phase cost and which one failed.
//
// It is written from gRPC's dial goroutine and read from the checker's, hence
// the mutex: grpc.NewClient hands the dialer to a background transport and
// there is no happens-before edge between that write and our read other than
// the connectivity-state change we wait on.
type phaseTimer struct {
	mu sync.Mutex

	dns     time.Duration
	hasDNS  bool
	connect time.Duration
	hasConn bool
	tls     time.Duration
	hasTLS  bool

	// failedPhase/err describe the FIRST failure observed. gRPC retries a
	// failed connection with backoff, so keeping the first one is what makes
	// the reported error the one that actually explains the outage.
	failedPhase string
	err         error

	// address is the endpoint the dialer actually dialed (an IP:port when we
	// resolved it ourselves). It is what locates a NetworkFailure marker.
	address string
}

func (p *phaseTimer) recordDNS(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.dns, p.hasDNS = d, true
}

func (p *phaseTimer) recordConnect(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.connect, p.hasConn = d, true
}

func (p *phaseTimer) recordTLS(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.tls, p.hasTLS = d, true
}

func (p *phaseTimer) recordAddress(addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.address = addr
}

// fail records a phase failure, keeping the first one seen.
func (p *phaseTimer) fail(phase string, err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.failedPhase == "" {
		p.failedPhase, p.err = phase, err
	}

	return err
}

// failure returns the first recorded failure, or ("", nil) when the connection
// never failed a phase of its own.
func (p *phaseTimer) failure() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.failedPhase, p.err
}

func (p *phaseTimer) dialedAddress() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.address
}

// applyMetrics writes the phases that actually happened onto the metrics map.
// A phase that did not run (DNS for an IP literal or a tunneled dial, TLS for
// h2c) records NO key rather than a zero — a 0 ms measurement and "this phase
// does not exist here" are different facts, and only the absent key says the
// second one.
func (p *phaseTimer) applyMetrics(metrics map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.hasDNS {
		metrics["dns_time_ms"] = durationMs(p.dns)
	}

	if p.hasConn {
		metrics["connect_time_ms"] = durationMs(p.connect)
	}

	if p.hasTLS {
		metrics["tls_time_ms"] = durationMs(p.tls)
	}
}

// instrumentedDialer builds the grpc.WithContextDialer that does the probe's
// own resolution and dialing, so each phase can be timed and attributed.
//
// Untunneled it resolves the hostname itself (skipped for an IP literal) and
// then dials the resolved address. Tunneled it hands the verbatim host:port to
// the bastion dialer and records no DNS time at all: resolution happens on the
// remote side by design, so a local lookup would both be wrong and defeat the
// point of the tunnel.
func instrumentedDialer(
	timer *phaseTimer,
	tunnelDialer checkerdef.ContextDialer,
) func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, addr string) (net.Conn, error) {
		if tunnelDialer != nil {
			timer.recordAddress(addr)

			start := time.Now()

			conn, err := tunnelDialer.DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, timer.fail(phaseConnect, err)
			}

			timer.recordConnect(time.Since(start))

			return conn, nil
		}

		candidates, err := resolveAddresses(ctx, timer, addr)
		if err != nil {
			return nil, err
		}

		return dialFirstReachable(ctx, timer, candidates)
	}
}

// dialFirstReachable tries each resolved address in order, the way gRPC's own
// resolver+balancer would, so a name with an unreachable AAAA record still
// connects over its A record instead of failing outright. The recorded
// connect time is that of the address that actually answered.
func dialFirstReachable(
	ctx context.Context,
	timer *phaseTimer,
	candidates []string,
) (net.Conn, error) {
	dialer := &net.Dialer{}

	var lastErr error

	for _, candidate := range candidates {
		timer.recordAddress(candidate)

		start := time.Now()

		conn, err := dialer.DialContext(ctx, "tcp", candidate)
		if err != nil {
			lastErr = err

			if ctx.Err() != nil {
				break
			}

			continue
		}

		timer.recordConnect(time.Since(start))

		return conn, nil
	}

	if lastErr == nil {
		lastErr = errConnectionFailed
	}

	return nil, timer.fail(phaseConnect, lastErr)
}

// resolveAddresses turns host:port into the ip:port candidates to dial, timing
// the lookup. An address that is already a literal IP is returned untouched
// with no DNS metric — there was no resolution to measure.
func resolveAddresses(ctx context.Context, timer *phaseTimer, addr string) ([]string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, timer.fail(phaseDNS, err)
	}

	if net.ParseIP(host) != nil {
		return []string{addr}, nil
	}

	start := time.Now()

	resolver := &net.Resolver{}

	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, timer.fail(phaseDNS, err)
	}

	timer.recordDNS(time.Since(start))

	if len(ips) == 0 {
		return nil, timer.fail(phaseDNS, &net.DNSError{
			Err:        "no such host",
			Name:       host,
			IsNotFound: true,
		})
	}

	candidates := make([]string, 0, len(ips))
	for _, ip := range ips {
		candidates = append(candidates, net.JoinHostPort(ip.IP.String(), port))
	}

	return candidates, nil
}

// timedCredentials wraps transport credentials to measure the TLS handshake.
// It exists because gRPC does the handshake inside its transport, well out of
// reach of anything the checker can time from the outside.
type timedCredentials struct {
	credentials.TransportCredentials

	timer *phaseTimer
}

func (t *timedCredentials) ClientHandshake(
	ctx context.Context,
	authority string,
	rawConn net.Conn,
) (net.Conn, credentials.AuthInfo, error) {
	start := time.Now()

	conn, info, err := t.TransportCredentials.ClientHandshake(ctx, authority, rawConn)
	if err != nil {
		return nil, nil, t.timer.fail(phaseTLS, err)
	}

	t.timer.recordTLS(time.Since(start))

	return conn, info, nil
}

// Clone keeps the timing wrapper in place across gRPC's internal clone of the
// credentials — without it the handshake would be timed by the original and
// the wrapper silently dropped.
func (t *timedCredentials) Clone() credentials.TransportCredentials {
	return &timedCredentials{
		TransportCredentials: t.TransportCredentials.Clone(),
		timer:                t.timer,
	}
}

// transportCredentials returns the (possibly timed) credentials for the check:
// TLS with optional verification skipping, or h2c plaintext.
func transportCredentials(cfg *GRPCConfig, timer *phaseTimer) credentials.TransportCredentials {
	if !cfg.TLS {
		return insecure.NewCredentials()
	}

	return &timedCredentials{
		TransportCredentials: credentials.NewTLS(&tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.TLSSkipVerify, //nolint:gosec // opt-in per check
		}),
		timer: timer,
	}
}

// errConnectionFailed is the fallback when gRPC reports TransientFailure but
// our own dialer/handshake recorded nothing — e.g. an HTTP/2 settings exchange
// that failed after a clean TCP connect and TLS handshake.
var errConnectionFailed = errors.New("connection could not be established")

// waitForReady drives the lazy client to an actual connection.
//
// grpc.NewClient does not dial; without this the first RPC absorbs DNS, TCP,
// TLS and the HTTP/2 handshake, which is exactly the fiction this replaces. We
// stop at the first TransientFailure rather than letting gRPC retry with
// backoff: the check has a deadline, and the first error is the one that
// explains the failure.
func waitForReady(ctx context.Context, conn *grpc.ClientConn, timer *phaseTimer) error {
	conn.Connect()

	for {
		state := conn.GetState()

		switch state {
		case connectivity.Ready:
			return nil
		case connectivity.TransientFailure, connectivity.Shutdown:
			_, err := timer.failure()
			if err != nil {
				return err
			}

			return errConnectionFailed
		case connectivity.Idle, connectivity.Connecting:
			// Keep waiting below.
		}

		if !conn.WaitForStateChange(ctx, state) {
			return ctx.Err()
		}
	}
}
