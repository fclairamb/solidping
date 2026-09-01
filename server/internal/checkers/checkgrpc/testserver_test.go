package checkgrpc_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
)

// testServerOptions configures the in-process health server the checker tests
// probe. Everything is off by default: a plaintext (h2c) server serving
// SERVING for the overall health of the server.
type testServerOptions struct {
	// tls serves TLS with a self-signed 127.0.0.1/localhost certificate.
	tls bool
	// tlsHandshakeDelay stalls the TLS handshake itself (in GetConfigForClient,
	// which runs before the handshake completes). It is the only server-side
	// delay that lands in the TLS phase, which is what makes it the proof that
	// the phase split is real.
	tlsHandshakeDelay time.Duration
	// rpcDelay stalls the health handler, landing squarely in rpc_time_ms.
	rpcDelay time.Duration
	// statuses registers per-service serving statuses. The empty key is the
	// overall server status; absent from the map means the health server
	// answers NOT_FOUND for that name.
	statuses map[string]healthpb.HealthCheckResponse_ServingStatus
	// noHealthService starts a server with no health service registered at all,
	// so every Check answers NOT_FOUND / Unimplemented.
	noHealthService bool
}

// testServer is a started in-process gRPC health server.
type testServer struct {
	host string
	port int

	mu       sync.Mutex
	lastMeta metadata.MD
}

// lastMetadata returns the metadata the server saw on the most recent RPC.
func (s *testServer) lastMetadata(t *testing.T) metadata.MD {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.lastMeta
}

// startTestServer boots a health server on a loopback port and tears it down
// with the test.
func startTestServer(t *testing.T, opts testServerOptions) *testServer {
	t.Helper()

	srv := &testServer{}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok, "loopback listener must expose a *net.TCPAddr")

	srv.host = addr.IP.String()
	srv.port = addr.Port

	serverOpts := []grpc.ServerOption{
		grpc.UnaryInterceptor(srv.interceptor(opts.rpcDelay)),
	}

	if opts.tls {
		serverOpts = append(serverOpts, grpc.Creds(
			credentials.NewTLS(testTLSConfig(t, opts.tlsHandshakeDelay)),
		))
	}

	grpcServer := grpc.NewServer(serverOpts...)

	if !opts.noHealthService {
		healthServer := health.NewServer()
		for service, status := range opts.statuses {
			healthServer.SetServingStatus(service, status)
		}

		if _, hasOverall := opts.statuses[""]; !hasOverall {
			healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
		}

		healthpb.RegisterHealthServer(grpcServer, healthServer)
	}

	go func() { _ = grpcServer.Serve(listener) }()

	t.Cleanup(grpcServer.Stop)

	return srv
}

// interceptor records the incoming metadata (so a test can assert exactly what
// the checker sent) and optionally stalls the handler.
func (s *testServer) interceptor(delay time.Duration) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			s.mu.Lock()
			s.lastMeta = md.Copy()
			s.mu.Unlock()
		}

		if delay > 0 {
			time.Sleep(delay)
		}

		return handler(ctx, req)
	}
}

// baseConfig is the config map every test starts from: this server, a short
// timeout so a hang fails fast rather than hanging the suite.
func (s *testServer) baseConfig() map[string]any {
	return map[string]any{
		"host":    s.host,
		"port":    float64(s.port),
		"timeout": "5s",
	}
}

// testTLSConfig mints a self-signed certificate for 127.0.0.1/localhost. The
// handshake delay is injected through GetConfigForClient, which the server runs
// after reading the ClientHello and before finishing the handshake — so the
// client sees the stall inside its TLS phase and nowhere else.
func testTLSConfig(t *testing.T, handshakeDelay time.Duration) *tls.Config {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2"},
	}

	if handshakeDelay > 0 {
		cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
			time.Sleep(handshakeDelay)

			return nil, nil //nolint:nilnil // nil config means "keep the base one"
		}
	}

	return cfg
}

// closedPort returns a loopback address nothing listens on: the listener is
// opened only to reserve a port the OS just handed out, then closed, so a
// connection to it is refused rather than filtered.
func closedPort(t *testing.T) (string, int) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	require.NoError(t, listener.Close())

	return addr.IP.String(), addr.Port
}

// metricMs reads a phase metric as float milliseconds, failing the test when
// the key is absent or not a number.
func metricMs(t *testing.T, metrics map[string]any, key string) float64 {
	t.Helper()

	raw, ok := metrics[key]
	require.Truef(t, ok, "metric %q is missing from %v", key, metrics)

	value, ok := raw.(float64)
	require.Truef(t, ok, "metric %q is %T, want float64", key, raw)

	return value
}

// hostPort renders a loopback endpoint for error-message assertions.
func hostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
