package checkgrpc_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkgrpc"
)

// execute runs the checker against a config map, failing on a transport-level
// error (which the checker reserves for "this config cannot be executed at
// all", never for a probe failure).
func execute(t *testing.T, configMap map[string]any) *checkerdef.Result {
	t.Helper()

	config := &checkgrpc.GRPCConfig{}
	require.NoError(t, config.FromMap(configMap))
	require.NoError(t, config.Validate())

	checker := &checkgrpc.GRPCChecker{}

	result, err := checker.Execute(t.Context(), config)
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

func TestServingIsUpWithPhaseMetrics(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{})

	result := execute(t, srv.baseConfig())

	r.Equal(checkerdef.StatusUp, result.Status)
	r.Equal("SERVING", result.Output["servingStatus"])

	// A literal IP has no name to resolve, so dns_time_ms is deliberately
	// ABSENT rather than 0 — the distinction is the whole point of the split.
	r.NotContains(result.Metrics, "dns_time_ms")
	// h2c: no TLS phase happened, so no key.
	r.NotContains(result.Metrics, "tls_time_ms")
	// The bogus metric this spec removed must not come back.
	r.NotContains(result.Metrics, "connection_time_ms")

	connect := metricMs(t, result.Metrics, "connect_time_ms")
	rpc := metricMs(t, result.Metrics, "rpc_time_ms")
	total := metricMs(t, result.Metrics, "total_time_ms")

	r.Positive(connect)
	r.Positive(rpc)
	r.GreaterOrEqual(total, connect)
	r.GreaterOrEqual(total, rpc)
}

// A hostname exercises the resolution phase that an IP literal skips.
func TestHostnameRecordsDNSPhase(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{})

	cfg := srv.baseConfig()
	cfg["host"] = "localhost"

	result := execute(t, cfg)

	r.Equal(checkerdef.StatusUp, result.Status)
	r.Contains(result.Metrics, "dns_time_ms")
}

// NOT_SERVING is a real, measured round-trip: the service answered, it just
// said it is draining. Losing its latency would hide a service slowing down
// before it goes out of rotation, so the metrics MUST survive the down verdict.
func TestNotServingIsDownAndKeepsItsLatency(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{
		statuses: map[string]healthpb.HealthCheckResponse_ServingStatus{
			"": healthpb.HealthCheckResponse_NOT_SERVING,
		},
	})

	result := execute(t, srv.baseConfig())

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("NOT_SERVING", result.Output["servingStatus"])
	r.Equal("service status: NOT_SERVING", result.Output["error"])

	r.Positive(metricMs(t, result.Metrics, "rpc_time_ms"))
	r.Positive(metricMs(t, result.Metrics, "total_time_ms"))
	r.Positive(metricMs(t, result.Metrics, "connect_time_ms"))
}

// A named service the health server knows about but reports as unknown.
func TestServiceUnknownIsDownWithMetrics(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{
		statuses: map[string]healthpb.HealthCheckResponse_ServingStatus{
			"my.service.v1": healthpb.HealthCheckResponse_SERVICE_UNKNOWN,
		},
	})

	cfg := srv.baseConfig()
	cfg["serviceName"] = "my.service.v1"

	result := execute(t, cfg)

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("SERVICE_UNKNOWN", result.Output["servingStatus"])
	r.Positive(metricMs(t, result.Metrics, "rpc_time_ms"))
}

// The most common gRPC health misconfiguration: a service name the server never
// registered. gRPC answers NOT_FOUND, which used to surface verbatim as
// `rpc error: code = NotFound desc = unknown service` and told an operator
// nothing about what to fix.
func TestUnregisteredServiceGetsAFriendlyMessage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{})

	cfg := srv.baseConfig()
	cfg["serviceName"] = "never.registered.v1"

	result := execute(t, cfg)

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(
		`service "never.registered.v1" is not registered with the health server`,
		result.Output["error"],
	)
	// The raw error stays available for anyone who wants it.
	r.Contains(result.Output["rpcError"], "NotFound")
	r.Equal("rpc", result.Output["phase"])
}

// A server with no health service at all is a different fix (implement the
// health protocol), so it gets a different message.
func TestServerWithoutHealthServiceSaysSo(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{noHealthService: true})

	result := execute(t, srv.baseConfig())

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("rpc", result.Output["phase"])
	r.Contains(result.Output["error"], "health check failed")
}

func TestTLSServerWithSkipVerify(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{tls: true})

	cfg := srv.baseConfig()
	cfg["tls"] = true
	cfg["tlsSkipVerify"] = true

	result := execute(t, cfg)

	r.Equal(checkerdef.StatusUp, result.Status)
	r.Positive(metricMs(t, result.Metrics, "tls_time_ms"))
	r.Equal(true, result.Output["tls"])
}

// Without skip-verify the self-signed certificate must be REJECTED — the
// positive control proving tlsSkipVerify actually does something.
func TestTLSServerWithoutSkipVerifyFailsTheHandshake(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{tls: true})

	cfg := srv.baseConfig()
	cfg["tls"] = true

	result := execute(t, cfg)

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("tls-handshake", result.Output["phase"])
	r.Contains(result.Output["error"], "tls handshake failed")

	// A certificate that does not validate is an APPLICATION-level answer —
	// the server is right there and talking — so it must not mint a
	// reachability marker and trigger a path trace.
	if result.Diagnostics != nil {
		r.Nil(result.Diagnostics.NetworkFailure)
	}
}

// TLS against a plaintext server: the handshake never completes.
func TestTLSAgainstPlaintextServerFailsInTheHandshake(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{})

	cfg := srv.baseConfig()
	cfg["tls"] = true
	cfg["tlsSkipVerify"] = true
	cfg["timeout"] = "3s"

	result := execute(t, cfg)

	r.NotEqual(checkerdef.StatusUp, result.Status)
	r.Equal("tls-handshake", result.Output["phase"])
}

// A name that cannot resolve dies in the DNS phase and says so, instead of
// arriving as an opaque `rpc error: …`.
func TestUnresolvableHostFailsInTheDNSPhase(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := execute(t, map[string]any{
		"host":    "grpc.nonexistent.invalid",
		"port":    float64(50051),
		"timeout": "3s",
	})

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("dns", result.Output["phase"])
	r.Contains(result.Output["error"], "dns resolution failed")
	r.NotContains(result.Metrics, "dns_time_ms")
	r.NotContains(result.Metrics, "connect_time_ms")

	// A name with no address has nothing to trace to.
	if result.Diagnostics != nil {
		r.Nil(result.Diagnostics.NetworkFailure)
	}
}

// A closed port dies in the connect phase and carries the reachability marker
// that lets the incident run a path trace.
func TestClosedPortFailsInTheConnectPhase(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := closedPort(t)

	result := execute(t, map[string]any{
		"host":    host,
		"port":    float64(port),
		"timeout": "3s",
	})

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("connect", result.Output["phase"])
	r.Contains(result.Output["error"], "connection failed")
	r.NotContains(result.Metrics, "connect_time_ms")

	r.NotNil(result.Diagnostics)
	r.NotNil(result.Diagnostics.NetworkFailure)
	r.Equal(checkerdef.NetFailureConnectionRefused, result.Diagnostics.NetworkFailure.Class)
	r.Equal(host, result.Diagnostics.NetworkFailure.Address)
	r.Equal(port, result.Diagnostics.NetworkFailure.Port)
	r.Equal(hostPort(host, port), hostPort(
		result.Diagnostics.NetworkFailure.Host,
		result.Diagnostics.NetworkFailure.Port,
	))
}

// ── The timing split is real, not cosmetic ────────────────────────────────
//
// Two servers that stall in DIFFERENT phases must move DIFFERENT metrics. Under
// the old lazy-dial code every one of these delays landed in rpc_time_ms,
// because grpc.NewClient dialed nothing and the first RPC absorbed DNS, TCP,
// TLS and the HTTP/2 handshake alike.

const phaseDelay = 400 * time.Millisecond

func TestASlowHandlerMovesOnlyTheRPCMetric(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{rpcDelay: phaseDelay})

	result := execute(t, srv.baseConfig())

	r.Equal(checkerdef.StatusUp, result.Status)
	r.GreaterOrEqual(metricMs(t, result.Metrics, "rpc_time_ms"), float64(phaseDelay.Milliseconds()))
	r.Less(metricMs(t, result.Metrics, "connect_time_ms"), float64(phaseDelay.Milliseconds()))
}

// The direct proof that connection cost is no longer inside rpc_time_ms: the
// server stalls the TLS handshake, and only tls_time_ms moves.
func TestASlowTLSHandshakeMovesOnlyTheTLSMetric(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{tls: true, tlsHandshakeDelay: phaseDelay})

	cfg := srv.baseConfig()
	cfg["tls"] = true
	cfg["tlsSkipVerify"] = true

	result := execute(t, cfg)

	r.Equal(checkerdef.StatusUp, result.Status)

	delayMs := float64(phaseDelay.Milliseconds())
	r.GreaterOrEqual(metricMs(t, result.Metrics, "tls_time_ms"), delayMs)
	r.Less(metricMs(t, result.Metrics, "rpc_time_ms"), delayMs,
		"the TLS handshake cost leaked into rpc_time_ms — the phase split is cosmetic")
	r.Less(metricMs(t, result.Metrics, "connect_time_ms"), delayMs)
}

// The deprecated keyword matcher still decodes and still behaves, so checks
// created before it was hidden from the dashboard keep working.
func TestDeprecatedKeywordStillMatches(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := startTestServer(t, testServerOptions{})

	cfg := srv.baseConfig()
	cfg["keyword"] = "SERVING"

	r.Equal(checkerdef.StatusUp, execute(t, cfg).Status)

	cfg["keyword"] = "NOPE"
	result := execute(t, cfg)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("keyword check failed", result.Output["error"])
}
