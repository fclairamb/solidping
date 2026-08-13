package tlsedge

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
)

// acmeE2EDomain is the hostname the edge is allowed to obtain a certificate
// for; acmeE2EDenied is the negative control that must never reach the CA.
const (
	acmeE2EDomain = "status-e2e.example.com"
	acmeE2EDenied = "unverified-e2e.example.com"
)

// TestACMEEndToEndWithPebble drives a REAL ACME issuance against Pebble —
// including real challenge validation over the network, back into this
// process's own listeners (see the plumbing in pebble_test.go) — and asserts
// the four properties the spec requires of in-server TLS:
//
//  1. a certificate is issued on the first TLS handshake (on-demand),
//  2. it is persisted in tls_storage,
//  3. the second handshake is served from what was persisted, with no
//     re-issuance, and
//  4. an unverified domain is denied — the handshake fails, NOTHING is written
//     to storage and the CA never even hears the name, i.e. no CA traffic at
//     all (the negative control that protects Let's Encrypt's
//     failed-validation rate limit).
//
// Because Pebble validates for real, issuance succeeding is itself proof that
// the Edge's own solver wiring works: its :80 ACME challenge handler and/or the
// "acme-tls/1" ALPN path on its :443 listener answered the CA.
//
// Skips when Docker is unavailable.
//
//nolint:paralleltest // owns a Docker container and two listeners
func TestACMEEndToEndWithPebble(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ACME end-to-end test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	// The CA has to be told which ports to validate against, and the Edge has to
	// bind exactly those, so both are chosen up front.
	httpPort, err := freePort(ctx)
	r.NoError(err)

	httpsPort, err := freePort(ctx)
	r.NoError(err)

	resolver := startTestDNS(ctx, t)
	ca := startPebble(ctx, t, resolver, httpPort, httpsPort)

	store, dbSvc := newTestStorage(t)

	edge, httpsAddr := startTestEdge(ctx, t, dbSvc, ca, httpPort, httpsPort)

	// --- 1. First handshake issues on demand -------------------------------
	state := handshake(ctx, t, httpsAddr, acmeE2EDomain)
	r.NotEmpty(state.PeerCertificates, "the handshake must present a certificate")

	leaf := state.PeerCertificates[0]
	r.Contains(leaf.DNSNames, acmeE2EDomain)
	r.NotEqual(leaf.Subject.String(), leaf.Issuer.String(), "the certificate must be CA-issued, not self-signed")

	// The CA only issues after a challenge it validated over the network. Its
	// log therefore proves the Edge's own solver answered: the authorization
	// went through real validation attempts (dns-01 cannot succeed here — the
	// test resolver serves no TXT records), and one of the Edge's two solvers
	// completed it.
	validationLog := ca.Logs(ctx, t)
	r.Contains(validationLog, acmeE2EDomain,
		"the CA must have done real work for the domain (validation is not short-circuited)")
	r.Regexp(`Starting \d+ validations`, validationLog,
		"the CA must have run real challenge validations")
	r.Contains(validationLog, "set VALID by completed challenge",
		"an Edge solver must have answered the CA's validation request")

	// --- 2. It is persisted in tls_storage ---------------------------------
	certKeys, err := store.List(ctx, "certificates", true)
	r.NoError(err)

	var (
		certKey string
		keyKey  string
	)

	for _, key := range certKeys {
		switch {
		case strings.HasSuffix(key, "/"+acmeE2EDomain+"/"+acmeE2EDomain+".crt"):
			certKey = key
		case strings.HasSuffix(key, "/"+acmeE2EDomain+"/"+acmeE2EDomain+".key"):
			keyKey = key
		}
	}

	r.NotEmpty(certKey, "the issued certificate must be persisted in tls_storage")
	r.NotEmpty(keyKey, "the private key must be persisted in tls_storage")

	// The ACME account material lands in storage too, proving a real
	// registration happened rather than a self-signed shortcut.
	accountKeys, err := store.List(ctx, "acme", true)
	r.NoError(err)
	r.NotEmpty(accountKeys)

	r.Equal(CertStatusIssued, edge.CertStatus(acmeE2EDomain))

	certInfo, err := store.Stat(ctx, certKey)
	r.NoError(err)

	// --- 3. Second handshake reuses it, no re-issuance ---------------------
	second := handshake(ctx, t, httpsAddr, acmeE2EDomain)
	r.NotEmpty(second.PeerCertificates)
	r.Equal(leaf.SerialNumber, second.PeerCertificates[0].SerialNumber,
		"the second handshake must serve the SAME certificate")

	afterInfo, err := store.Stat(ctx, certKey)
	r.NoError(err)
	r.Equal(certInfo.Modified, afterInfo.Modified, "a reused certificate must not be rewritten")

	// --- 4. Negative control: an unverified domain never reaches the CA ----
	before, err := store.List(ctx, "certificates", true)
	r.NoError(err)

	// The gate refuses before any protocol work happens...
	r.ErrorIs(edge.Decide(ctx, acmeE2EDenied), ErrHostNotAllowed)

	// ...and the handshake itself fails.
	_, handshakeErr := dial(ctx, httpsAddr, acmeE2EDenied)
	r.Error(handshakeErr, "an unverified domain must not get a certificate")

	after, err := store.List(ctx, "certificates", true)
	r.NoError(err)
	r.Equal(before, after, "a denied domain must produce no storage writes (hence no CA traffic)")

	for _, key := range after {
		r.NotContains(key, acmeE2EDenied)
	}

	// The CA is the authority on "no CA traffic": it never saw the name at all,
	// so no order, no authorization and no failed validation were recorded
	// against it.
	r.NotContains(ca.Logs(ctx, t), acmeE2EDenied,
		"a denied domain must never reach the CA")

	r.Equal(CertStatusNone, edge.CertStatus(acmeE2EDenied))
}

// startTestEdge builds an Edge pointed at the Pebble CA, binds it on the given
// ports, and returns it with the HTTPS address to dial. The listeners bind all
// interfaces (not just loopback) because the CA validates from inside a
// container and has to reach them.
func startTestEdge(
	ctx context.Context, t *testing.T, dbSvc db.Service, ca *pebbleCA, httpPort, httpsPort string,
) (*Edge, string) {
	t.Helper()
	r := require.New(t)

	edge, err := New(&Options{
		ACME: config.ACMEConfig{
			Enabled:     true,
			Email:       "acme-e2e@solidping.test",
			CAURL:       ca.DirectoryURL,
			ListenHTTP:  "0.0.0.0:" + httpPort,
			ListenHTTPS: "0.0.0.0:" + httpsPort,
		},
		DB: dbSvc,
		// No reserved hosts: the ONLY allowed name comes from the
		// custom-domain gate, so the negative control is unambiguous.
		CustomDomainServable: func(_ context.Context, host string) bool {
			return host == acmeE2EDomain
		},
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusNoContent)
		}),
		TrustedRoots: ca.Roots,
	})
	r.NoError(err)
	r.NoError(edge.Start(ctx))

	// contextcheck: cleanup runs after the test's context is canceled, so the
	// drain needs its own.
	t.Cleanup(func() { //nolint:contextcheck
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = edge.Shutdown(shutdownCtx)
	})

	addr := "127.0.0.1:" + httpsPort
	waitForListener(ctx, t, addr)

	return edge, addr
}

// waitForListener blocks until the given address accepts TCP connections.
func waitForListener(ctx context.Context, t *testing.T, addr string) {
	t.Helper()

	dialer := &net.Dialer{Timeout: time.Second}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()

			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("listener %s never came up", addr)
}

// dial performs a TLS handshake against the edge with the given SNI. The client
// deliberately does not verify the chain (Pebble's issuing root is not in any
// system pool); the test inspects the presented leaf instead.
func dial(ctx context.Context, addr, serverName string) (tls.ConnectionState, error) {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 30 * time.Second},
		Config: &tls.Config{
			ServerName: serverName,
			// The test asserts the presented leaf explicitly; Pebble's issuing
			// root is deliberately not in any system pool.
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		},
	}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return tls.ConnectionState{}, err
	}

	defer func() { _ = conn.Close() }()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return tls.ConnectionState{}, errNotTLSConn
	}

	return tlsConn.ConnectionState(), nil
}

// errNotTLSConn guards the type assertion on the dialed connection.
var errNotTLSConn = errors.New("dialed connection is not a TLS connection")

// handshake is dial with a fatal assertion, for the success paths.
func handshake(ctx context.Context, t *testing.T, addr, serverName string) tls.ConnectionState {
	t.Helper()

	state, err := dial(ctx, addr, serverName)
	require.NoError(t, err, "handshake for %s", serverName)

	return state
}

// chainedE2EDomain is owned by the DOWNSTREAM instance in the chained test: the
// CA only ever talks to the upstream's ports, so every challenge for it has to
// travel through the fallback to be solved.
const chainedE2EDomain = "chained-e2e.example.com"

// TestACMEChainedFallbackWithPebble is the chained-deployment claim end to end,
// against a real ACME CA: two instances behind ONE catch-all port pair, where
// the second instance obtains and serves a certificate for a domain the first
// has never heard of.
//
// The CA validates against the UPSTREAM's :80/:443 only (that is what a single
// Traefik catch-all can offer), so issuance can only succeed if the SNI peek
// forwards the TLS-ALPN-01 handshake and the Host peek forwards the HTTP-01
// request, both unterminated, to the downstream. It also pins the other half:
// the upstream keeps serving its OWN domain from its own storage, on the same
// listeners, at the same time.
//
// Skips when Docker is unavailable.
//
//nolint:paralleltest // owns a Docker container and four listeners
func TestACMEChainedFallbackWithPebble(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ACME end-to-end test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	// The ports the CA validates against are the UPSTREAM's — the single slot a
	// catch-all edge can forward to.
	httpPort, err := freePort(ctx)
	r.NoError(err)

	httpsPort, err := freePort(ctx)
	r.NoError(err)

	downHTTPPort, err := freePort(ctx)
	r.NoError(err)

	downHTTPSPort, err := freePort(ctx)
	r.NoError(err)

	resolver := startTestDNS(ctx, t)
	ca := startPebble(ctx, t, resolver, httpPort, httpsPort)

	downStore, downDB := newTestStorage(t)
	upStore, upDB := newTestStorage(t)

	// The second hop: reachable only from the first, and the only instance that
	// knows chainedE2EDomain.
	startChainedEdge(ctx, t, chainedEdgeOptions{
		db: downDB, ca: ca,
		listenHTTP: "127.0.0.1:" + downHTTPPort, listenHTTPS: "127.0.0.1:" + downHTTPSPort,
		owns: chainedE2EDomain,
		// The upstream forwards with a PROXY v2 header; trusting loopback is
		// exactly the downstream-trust step the runbook describes.
		trustProxyFrom: []string{loopbackCIDR},
	})

	// The first hop: what the CA (and a real client) actually talks to.
	upstream := startChainedEdge(ctx, t, chainedEdgeOptions{
		db: upDB, ca: ca,
		listenHTTP: "0.0.0.0:" + httpPort, listenHTTPS: "0.0.0.0:" + httpsPort,
		owns:                  acmeE2EDomain,
		fallbackUpstreamHTTP:  "127.0.0.1:" + downHTTPPort,
		fallbackUpstreamHTTPS: "127.0.0.1:" + downHTTPSPort,
	})

	edgeAddr := "127.0.0.1:" + httpsPort
	waitForListener(ctx, t, edgeAddr)

	// --- the downstream's domain is issued and served THROUGH the chain ----
	state := handshake(ctx, t, edgeAddr, chainedE2EDomain)
	r.NotEmpty(state.PeerCertificates, "the chained handshake must present a certificate")

	leaf := state.PeerCertificates[0]
	r.Contains(leaf.DNSNames, chainedE2EDomain)
	r.NotEqual(leaf.Subject.String(), leaf.Issuer.String(),
		"the certificate must be CA-issued, not self-signed")

	validationLog := ca.Logs(ctx, t)
	r.Contains(validationLog, chainedE2EDomain,
		"the CA must have validated the domain for real, through the chain")
	r.Contains(validationLog, "set VALID by completed challenge",
		"a solver behind the fallback must have answered the CA")

	// The certificate belongs to the hop that owns the domain — the upstream
	// never held any key material for it.
	r.True(hasStoredCert(ctx, t, downStore, chainedE2EDomain),
		"the downstream must own the issued certificate")
	r.False(hasStoredCert(ctx, t, upStore, chainedE2EDomain),
		"the upstream must never hold a certificate for a domain it forwards")

	forwardedHTTPS := upstream.splitterHTTPS(t).stats.forwarded.Load()
	r.Positive(forwardedHTTPS, "the upstream must have forwarded the TLS connections")

	// --- the upstream keeps serving its OWN domain on the same listeners ---
	ownState := handshake(ctx, t, edgeAddr, acmeE2EDomain)
	r.NotEmpty(ownState.PeerCertificates)
	r.Contains(ownState.PeerCertificates[0].DNSNames, acmeE2EDomain)
	r.True(hasStoredCert(ctx, t, upStore, acmeE2EDomain),
		"the upstream's own domain must be issued and stored by the upstream")
	r.False(hasStoredCert(ctx, t, downStore, acmeE2EDomain),
		"a domain the upstream owns must never reach the downstream")
}

// chainedEdgeOptions configures one hop of the chained Pebble test.
type chainedEdgeOptions struct {
	db                    db.Service
	ca                    *pebbleCA
	listenHTTP            string
	listenHTTPS           string
	owns                  string
	fallbackUpstreamHTTP  string
	fallbackUpstreamHTTPS string
	trustProxyFrom        []string
}

// startChainedEdge builds and starts one hop against the Pebble CA.
func startChainedEdge(ctx context.Context, t *testing.T, opts chainedEdgeOptions) *chainEdge {
	t.Helper()
	r := require.New(t)

	acme := config.ACMEConfig{
		Enabled:                       true,
		Email:                         "acme-chain-e2e@solidping.test",
		CAURL:                         opts.ca.DirectoryURL,
		ListenHTTP:                    opts.listenHTTP,
		ListenHTTPS:                   opts.listenHTTPS,
		FallbackUpstreamHTTP:          opts.fallbackUpstreamHTTP,
		FallbackUpstreamHTTPS:         opts.fallbackUpstreamHTTPS,
		FallbackUpstreamProxyProtocol: true,
	}

	if len(opts.trustProxyFrom) > 0 {
		acme.ProxyProtocol = true
		acme.ProxyProtocolTrustedCIDRs = opts.trustProxyFrom
	}

	owns := opts.owns

	edge, err := New(&Options{
		ACME: acme,
		DB:   opts.db,
		// No reserved hosts: each hop owns exactly one domain, so "which hop
		// answered" is never ambiguous.
		CustomDomainServable: func(_ context.Context, host string) bool { return host == owns },
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusNoContent)
		}),
		TrustedRoots: opts.ca.Roots,
	})
	r.NoError(err)
	r.NoError(edge.Start(ctx))

	// contextcheck: cleanup runs after the test's context is canceled.
	t.Cleanup(func() { //nolint:contextcheck
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = edge.Shutdown(shutdownCtx)
	})

	return &chainEdge{
		edge:      edge,
		httpAddr:  edge.httpLn.Addr().String(),
		httpsAddr: edge.httpsLn.Addr().String(),
	}
}

// hasStoredCert reports whether a storage holds certificate material for the
// domain, which is how "the right hop owns the certificate" is asserted.
func hasStoredCert(ctx context.Context, t *testing.T, store *Storage, domain string) bool {
	t.Helper()

	keys, err := store.List(ctx, "certificates", true)
	if err != nil {
		return false
	}

	for _, key := range keys {
		if strings.HasSuffix(key, "/"+domain+"/"+domain+".crt") {
			return true
		}
	}

	return false
}
