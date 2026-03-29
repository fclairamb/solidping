// Package checkmongodb provides MongoDB database health checks.
package checkmongodb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// MongoDBChecker implements the Checker interface for MongoDB health checks.
type MongoDBChecker struct{}

// Type returns the check type identifier.
func (c *MongoDBChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeMongoDB
}

// Validate checks if the configuration is valid.
func (c *MongoDBChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &MongoDBConfig{}
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
		spec.Slug = "mongodb-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

// Execute performs the MongoDB ping check and returns the result.
func (c *MongoDBChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*MongoDBConfig](config)
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

	client, err := mongo.Connect(
		options.Client().
			ApplyURI(cfg.buildURI()).
			SetConnectTimeout(timeout).
			SetTimeout(timeout),
	)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   map[string]any{"error": "failed to create client: " + err.Error()},
		}, nil
	}

	defer func() { _ = client.Disconnect(ctx) }()

	return c.ping(ctx, client, cfg, start)
}

func (c *MongoDBChecker) ping(
	ctx context.Context,
	client *mongo.Client,
	cfg *MongoDBConfig,
	start time.Time,
) (*checkerdef.Result, error) { //nolint:unparam // error kept for interface consistency
	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}

	dbName := cfg.Database
	if dbName == "" {
		dbName = "admin"
	}

	pingStart := time.Now()

	var result bson.M

	err := client.Database(dbName).RunCommand(ctx, bson.D{{Key: "ping", Value: 1}}).Decode(&result)
	if err != nil {
		return handlePingError(ctx, err, start), nil
	}

	if ok, exists := result["ok"]; !exists || ok != float64(1) {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output:   map[string]any{"error": "ping command returned non-ok status"},
		}, nil
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics: map[string]any{
			"ping_time_ms":  durationMs(time.Since(pingStart)),
			"total_time_ms": durationMs(time.Since(start)),
		},
		Output: map[string]any{
			"host":   cfg.Host,
			"port":   port,
			"result": "ok",
		},
	}, nil
}

func handlePingError(ctx context.Context, err error, start time.Time) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Output:   map[string]any{"error": "connection timeout"},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output:   map[string]any{"error": "ping failed: " + err.Error()},
	}
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
