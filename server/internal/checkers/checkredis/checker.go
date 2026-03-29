// Package checkredis provides Redis server health checks.
package checkredis

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// RedisChecker implements the Checker interface for Redis health checks.
type RedisChecker struct{}

// Type returns the check type identifier.
func (c *RedisChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeRedis
}

// Validate checks if the configuration is valid.
func (c *RedisChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &RedisConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}

	if spec.Name == "" {
		spec.Name = fmt.Sprintf("%s:%d", cfg.Host, port)
	}

	if spec.Slug == "" {
		spec.Slug = "redis-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
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

	client := redis.NewClient(&redis.Options{
		Addr:        cfg.addr(),
		Password:    cfg.Password,
		DB:          cfg.Database,
		DialTimeout: timeout,
		ReadTimeout: timeout,
	})

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
