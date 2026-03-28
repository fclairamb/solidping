package checkgrpc

import (
	"context"
	"crypto/tls"
	"fmt"
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
//
//nolint:cyclop // Health check logic requires multiple branches
func (c *GRPCChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, ok := config.(*GRPCConfig)
	if !ok {
		return nil, ErrInvalidConfigType
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
	}

	if cfg.ServiceName != "" {
		output["service_name"] = cfg.ServiceName
	}

	output["tls"] = cfg.TLS

	// Build dial options
	var dialOpts []grpc.DialOption

	if cfg.TLS {
		tlsCfg := &tls.Config{
			InsecureSkipVerify: cfg.TLSSkipVerify, //nolint:gosec // User-configurable TLS verification
		}
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	// Connect
	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   map[string]any{"error": "failed to create client: " + err.Error()},
		}, nil
	}

	defer func() { _ = conn.Close() }()

	connectTime := time.Since(start)
	metrics["connection_time_ms"] = durationMs(connectTime)

	// Health check
	healthClient := healthpb.NewHealthClient(conn)
	rpcStart := time.Now()

	resp, err := healthClient.Check(ctx, &healthpb.HealthCheckRequest{
		Service: cfg.ServiceName,
	})

	if err != nil {
		return handleRPCError(ctx, err, start, metrics, output), nil
	}

	metrics["rpc_time_ms"] = durationMs(time.Since(rpcStart))
	metrics["total_time_ms"] = durationMs(time.Since(start))

	servingStatus := resp.GetStatus().String()
	output["serving_status"] = servingStatus

	// Check serving status
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		output["error"] = fmt.Sprintf("service status: %s", servingStatus)

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}, nil
	}

	// Keyword check
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
			}, nil
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}, nil
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
