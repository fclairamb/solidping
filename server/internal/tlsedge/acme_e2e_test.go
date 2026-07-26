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

// TestACMEEndToEndWithPebble drives a REAL ACME issuance against Pebble and
// asserts the four properties the spec requires of in-server TLS:
//
//  1. a certificate is issued on the first TLS handshake (on-demand),
//  2. it is persisted in tls_storage,
//  3. the second handshake is served from what was persisted, with no
//     re-issuance, and
//  4. an unverified domain is denied — the handshake fails and NOTHING is
//     written to storage, i.e. no CA traffic at all (the negative control that
//     protects Let's Encrypt's failed-validation rate limit).
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

	ca := startPebble(ctx, t)

	store, dbSvc := newTestStorage(t)

	edge, httpsAddr := startTestEdge(ctx, t, dbSvc, ca)

	// --- 1. First handshake issues on demand -------------------------------
	state := handshake(ctx, t, httpsAddr, acmeE2EDomain)
	r.NotEmpty(state.PeerCertificates, "the handshake must present a certificate")

	leaf := state.PeerCertificates[0]
	r.Contains(leaf.DNSNames, acmeE2EDomain)
	r.NotEqual(leaf.Subject.String(), leaf.Issuer.String(), "the certificate must be CA-issued, not self-signed")

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

	r.Equal(CertStatusNone, edge.CertStatus(acmeE2EDenied))
}

// startTestEdge builds an Edge pointed at the Pebble CA, binds it on ephemeral
// ports, and returns it with the HTTPS address to dial.
func startTestEdge(ctx context.Context, t *testing.T, dbSvc db.Service, ca *pebbleCA) (*Edge, string) {
	t.Helper()
	r := require.New(t)

	httpPort, err := freePort(ctx)
	r.NoError(err)

	httpsPort, err := freePort(ctx)
	r.NoError(err)

	edge, err := New(&Options{
		ACME: config.ACMEConfig{
			Enabled:     true,
			Email:       "acme-e2e@solidping.test",
			CAURL:       ca.DirectoryURL,
			ListenHTTP:  "127.0.0.1:" + httpPort,
			ListenHTTPS: "127.0.0.1:" + httpsPort,
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
