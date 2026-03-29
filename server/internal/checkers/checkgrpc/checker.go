// Package checkgrpc provides gRPC service health checks.
package checkgrpc

import (
	"context"
	"crypto/tls"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
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

// Execute performs the gRPC health check and returns the result.
func (c *GRPCChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*GRPCConfig](config)
	if err != nil {
		return nil, err
	}

	timeout := cfg.resolveTimeout()
	target := cfg.resolveTarget()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()

	metrics := map[string]any{}
	output := map[string]any{
		"host": cfg.Host,
		"port": cfg.resolvePort(),
		"tls":  cfg.TLS,
	}

	if cfg.ServiceName != "" {
		output["serviceName"] = cfg.ServiceName
	}

	conn, result := c.connect(cfg, target, start)
	if result != nil {
		return result, nil
	}

	defer func() { _ = conn.Close() }()

	metrics["connection_time_ms"] = durationMs(time.Since(start))

	return c.checkHealth(ctx, conn, cfg, start, metrics, output), nil
}

func (c *GRPCChecker) connect(
	cfg *GRPCConfig,
	target string,
	start time.Time,
) (*grpc.ClientConn, *checkerdef.Result) {
	var dialOpts []grpc.DialOption

	if cfg.TLS {
		tlsCfg := &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.TLSSkipVerify,
		}
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   map[string]any{"error": "failed to create client: " + err.Error()},
		}
	}

	return conn, nil
}

func (c *GRPCChecker) checkHealth(
	ctx context.Context,
	conn *grpc.ClientConn,
	cfg *GRPCConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	healthClient := healthpb.NewHealthClient(conn)
	rpcStart := time.Now()

	resp, err := healthClient.Check(ctx, &healthpb.HealthCheckRequest{
		Service: cfg.ServiceName,
	})
	if err != nil {
		return handleRPCError(ctx, err, start, metrics, output)
	}

	metrics["rpc_time_ms"] = durationMs(time.Since(rpcStart))
	metrics["total_time_ms"] = durationMs(time.Since(start))

	servingStatus := resp.GetStatus().String()
	output["servingStatus"] = servingStatus

	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		output["error"] = "service status: " + servingStatus

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	if cfg.Keyword != "" {
		found := strings.Contains(servingStatus, cfg.Keyword)
		if cfg.InvertKeyword {
			found = !found
		}

		if !found {
			output["error"] = "keyword check failed"

			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: time.Since(start),
				Metrics:  metrics,
				Output:   output,
			}
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func handleRPCError(
	ctx context.Context,
	err error,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	if ctx.Err() != nil {
		output["error"] = "connection timeout"

		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	output["error"] = "health check failed: " + err.Error()

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
