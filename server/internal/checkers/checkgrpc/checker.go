// Package checkgrpc provides gRPC service health checks.
package checkgrpc

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// GRPCChecker implements the Checker interface for gRPC health checks.
type GRPCChecker struct{}

// Type returns the check type identifier.
func (c *GRPCChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeGRPC
}

// Validate checks if the configuration is valid.
func (c *GRPCChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &GRPCConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		name := cfg.resolveTarget()
		if cfg.ServiceName != "" {
			name += "/" + cfg.ServiceName
		}

		spec.Name = name
	}

	if spec.Slug == "" {
		spec.Slug = "grpc-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

// execContext bundles the per-execution state every phase helper needs, so the
// helpers stay under the argument-count lint without smuggling state onto the
// (shared, stateless) checker value.
type execContext struct {
	cfg      *GRPCConfig
	timer    *phaseTimer
	start    time.Time
	metrics  map[string]any
	output   map[string]any
	tunneled bool
}

// Execute performs the gRPC health check and returns the result.
//
// The connection is established EAGERLY and instrumented. grpc.NewClient is
// lazy — it builds a struct and dials nothing — so the old code's
// `connection_time_ms`, measured right after it returned, timed struct
// construction while the real DNS/TCP/TLS cost hid inside the first RPC. Here
// each phase is measured where it happens, and a failure is reported as the
// phase that produced it rather than as an opaque `rpc error: …`.
func (c *GRPCChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*GRPCConfig](config)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, cfg.resolveTimeout())
	defer cancel()

	// The `dns` resolver gRPC uses by default would resolve the name BEFORE
	// our dialer ever sees it — `dns_time_ms` would measure nothing and every
	// resolution failure would surface as an opaque resolver error. The
	// `passthrough` resolver hands the raw host:port to the dialer, which is
	// what lets the checker time and classify resolution itself. It is also
	// what the tunneled path has always needed (resolution belongs on the far
	// side of the bastion), so both paths now share one target form.
	target := "passthrough:///" + cfg.resolveTarget()

	tunnelDialer := checkerdef.TunnelDialerFrom(ctx)

	exec := &execContext{
		cfg:     cfg,
		timer:   &phaseTimer{},
		start:   time.Now(),
		metrics: map[string]any{},
		output: map[string]any{
			"host": cfg.Host,
			"port": cfg.resolvePort(),
			"tls":  cfg.TLS,
		},
		tunneled: tunnelDialer != nil,
	}

	if cfg.ServiceName != "" {
		exec.output["serviceName"] = cfg.ServiceName
	}

	if exec.tunneled {
		exec.output["tunneled"] = true
	}

	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(transportCredentials(cfg, exec.timer)),
		grpc.WithContextDialer(instrumentedDialer(exec.timer, tunnelDialer)),
	)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(exec.start),
			Output:   map[string]any{"error": "failed to create client: " + err.Error()},
		}, nil
	}

	defer func() { _ = conn.Close() }()

	if connErr := waitForReady(ctx, conn, exec.timer); connErr != nil {
		return connectionFailure(ctx, exec, connErr), nil
	}

	exec.timer.applyMetrics(exec.metrics)

	return c.checkHealth(ctx, conn, exec), nil
}

// connectionFailure renders a failure that happened before the health RPC was
// ever sent, naming the phase it died in.
func connectionFailure(
	ctx context.Context,
	exec *execContext,
	connErr error,
) *checkerdef.Result {
	exec.timer.applyMetrics(exec.metrics)
	exec.metrics["total_time_ms"] = durationMs(time.Since(exec.start))

	phase, phaseErr := exec.timer.failure()
	if phase == "" {
		phase = phaseConnect
	}

	if phaseErr == nil {
		phaseErr = connErr
	}

	exec.output["phase"] = phase

	timedOut := ctx.Err() != nil
	if timedOut {
		exec.output["error"] = phaseLabel(phase) + " timed out"
	} else {
		exec.output["error"] = phaseLabel(phase) + " failed: " + phaseErr.Error()
	}

	result := &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(exec.start),
		Metrics:  exec.metrics,
		Output:   exec.output,
	}

	if timedOut {
		result.Status = checkerdef.StatusTimeout
	}

	attachNetworkFailure(result, exec, phase, phaseErr, timedOut)

	return result
}

// phaseLabel is the human half of a phase name, used to build the output error.
func phaseLabel(phase string) string {
	switch phase {
	case phaseDNS:
		return "dns resolution"
	case phaseTLS:
		return "tls handshake"
	case phaseRPC:
		return "health check"
	case phaseConnect:
		return "connection"
	default:
		return "connection"
	}
}

// attachNetworkFailure hangs a reachability marker on a connection failure so
// the incident can carry a path trace, reusing checkerdef's classifiers.
//
// Two deliberate omissions: a DNS failure has no address to trace to (which is
// why ClassifyDialError returns "" for one), and a tunneled failure happened on
// the far side of a bastion, so a trace run from this worker would describe a
// route the probe never took — the same reasoning the HTTP path uses.
func attachNetworkFailure(
	result *checkerdef.Result,
	exec *execContext,
	phase string,
	phaseErr error,
	timedOut bool,
) {
	if exec.tunneled {
		return
	}

	var class string

	switch phase {
	case phaseConnect:
		class = checkerdef.ClassifyDialError(phaseErr, timedOut)
	case phaseTLS:
		class = checkerdef.ClassifyTLSHandshakeError(phaseErr, timedOut)
	case phaseDNS, phaseRPC:
		return
	default:
		return
	}

	if class == "" {
		return
	}

	address, _ := splitDialedAddress(exec.timer.dialedAddress())
	result.SetNetworkFailure(checkerdef.NewNetworkFailure(
		class, exec.cfg.Host, address, exec.cfg.resolvePort(),
	))
}

// splitDialedAddress pulls the IP out of the "ip:port" the dialer used. A
// malformed or empty value yields an empty address rather than a bogus one.
func splitDialedAddress(dialed string) (string, int) {
	host, portStr, err := net.SplitHostPort(dialed)
	if err != nil {
		return "", 0
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return host, 0
	}

	return host, port
}

func (c *GRPCChecker) checkHealth(
	ctx context.Context,
	conn *grpc.ClientConn,
	exec *execContext,
) *checkerdef.Result {
	healthClient := healthpb.NewHealthClient(conn)

	rpcCtx := ctx
	if md := exec.cfg.EffectiveMetadata(); len(md) > 0 {
		rpcCtx = metadata.NewOutgoingContext(ctx, metadata.New(md))
	}

	rpcStart := time.Now()

	resp, err := healthClient.Check(rpcCtx, &healthpb.HealthCheckRequest{
		Service: exec.cfg.ServiceName,
	})
	if err != nil {
		return handleRPCError(ctx, err, exec)
	}

	// Recorded BEFORE the status branch on purpose: a NOT_SERVING answer is a
	// real, measured round-trip, and keeping its latency is what lets a service
	// be seen slowing down before it drains.
	exec.metrics["rpc_time_ms"] = durationMs(time.Since(rpcStart))
	exec.metrics["total_time_ms"] = durationMs(time.Since(exec.start))

	servingStatus := resp.GetStatus().String()
	exec.output["servingStatus"] = servingStatus

	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		exec.output["error"] = "service status: " + servingStatus

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(exec.start),
			Metrics:  exec.metrics,
			Output:   exec.output,
		}
	}

	if result := keywordFailure(exec, servingStatus); result != nil {
		return result
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(exec.start),
		Metrics:  exec.metrics,
		Output:   exec.output,
	}
}

// keywordFailure applies the deprecated keyword/invertKeyword matching against
// the serving-status enum string, returning nil when it passes or is unset.
//
// Deprecated behavior, kept decoding-compatible for checks created before the
// serving status made it redundant: it is not exposed by the dashboard and not
// documented as a supported option.
func keywordFailure(exec *execContext, servingStatus string) *checkerdef.Result {
	if exec.cfg.Keyword == "" {
		return nil
	}

	found := strings.Contains(servingStatus, exec.cfg.Keyword)
	if exec.cfg.InvertKeyword {
		found = !found
	}

	if found {
		return nil
	}

	exec.output["error"] = "keyword check failed"

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(exec.start),
		Metrics:  exec.metrics,
		Output:   exec.output,
	}
}

// handleRPCError renders a failure of the health RPC itself — the connection
// was up, so this is an application-level answer, never a reachability one.
func handleRPCError(
	ctx context.Context,
	err error,
	exec *execContext,
) *checkerdef.Result {
	exec.metrics["total_time_ms"] = durationMs(time.Since(exec.start))
	exec.output["phase"] = phaseRPC

	if ctx.Err() != nil {
		exec.output["error"] = "health check timed out"

		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(exec.start),
			Metrics:  exec.metrics,
			Output:   exec.output,
		}
	}

	exec.output["error"] = rpcErrorMessage(exec.cfg, err)
	exec.output["rpcError"] = err.Error()

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(exec.start),
		Metrics:  exec.metrics,
		Output:   exec.output,
	}
}

// rpcErrorMessage turns an RPC error into something an operator can act on.
//
// NOT_FOUND is the single most common gRPC health misconfiguration — checking a
// service name the server never registered — and `rpc error: code = NotFound
// desc = unknown service` says nothing about what to fix. The raw error is kept
// alongside, under output["rpcError"].
func rpcErrorMessage(cfg *GRPCConfig, err error) string {
	if status.Code(err) == codes.NotFound {
		name := cfg.ServiceName
		if name == "" {
			// An empty service name means "overall server health", which a
			// health server always answers; NOT_FOUND here means it does not
			// implement the health service at all.
			return "the server does not implement grpc.health.v1.Health"
		}

		return "service " + strconv.Quote(name) + " is not registered with the health server"
	}

	return "health check failed: " + err.Error()
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
