// Package checkredis provides Redis server health checks.
package checkredis

import (
	"context"
	"net"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkredis/config"
)

const microsecondsPerMilli = 1000.0

// RedisChecker implements the Checker interface for Redis health checks.
type RedisChecker struct{}

// Type returns the check type identifier.
func (c *RedisChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeRedis
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *RedisChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute performs the Redis PING check and returns the result.
func (c *RedisChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*RedisConfig](config)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()

	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}

	metrics := map[string]any{}
	output := map[string]any{
		"host": cfg.Host,
		"port": port,
	}

	if cfg.Socket != "" {
		output = map[string]any{"socket": cfg.Socket}
	}

	opts := &redis.Options{
		Addr:        cfg.Addr(),
		Password:    cfg.Password,
		DB:          cfg.Database,
		DialTimeout: timeout,
		ReadTimeout: timeout,
	}

	setRedisDialer(ctx, cfg, opts, timeout, output)

	client := redis.NewClient(opts)

	defer func() { _ = client.Close() }()

	pingStart := time.Now()

	result, err := client.Ping(ctx).Result()
	if err != nil {
		if ctx.Err() != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: time.Since(start),
				Output:   map[string]any{"error": "connection timeout"},
			}, nil
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output:   map[string]any{"error": "ping failed: " + err.Error()},
		}, nil
	}

	metrics["ping_time_ms"] = durationMs(time.Since(pingStart))
	metrics["total_time_ms"] = durationMs(time.Since(start))
	output["result"] = result

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}, nil
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}

// setRedisDialer picks the dial path: the shared Unix socket dialer, the SSH
// tunnel or the egress guard. Every non-socket path refuses a Unix network, so
// only the socket field reaches a socket.
func setRedisDialer(
	ctx context.Context, cfg *RedisConfig, opts *redis.Options, timeout time.Duration, output map[string]any,
) {
	if cfg.Socket != "" {
		// Unix socket (spec 2026-11-24-03): the shared dialer applies every
		// gate (tunnel, egress guard, path syntax, allowlist), never go-redis's
		// own socket support.
		opts.Network = "unix"
		opts.Addr = cfg.Socket
		opts.Dialer = checkerdef.LocalSocketDialer(ctx, cfg.Socket, timeout).DialContext
	} else if dialer := checkerdef.TunnelDialerFrom(ctx); dialer != nil {
		opts.Dialer = checkerdef.NoLocalSocketDialer(dialer).DialContext
		output["tunneled"] = true
	} else {
		// Egress guard (spec 2026-09-25-19), a pass-through when not enforcing.
		// The check sets no TLSConfig, so a (guarded) net.Dialer with go-redis's
		// DialTimeout is its default dial. go-redis would switch to `unix` for
		// an address starting with `/`: refused here (spec 2026-11-24-08).
		opts.Dialer = checkerdef.NoLocalSocketDialer(
			checkerdef.GuardedNetDialer(ctx, &net.Dialer{Timeout: timeout}),
		).DialContext
	}
}
