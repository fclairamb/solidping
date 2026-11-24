// Package checkmongodb provides MongoDB database health checks.
package checkmongodb

import (
	"context"
	"net"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkmongodb/config"
)

const microsecondsPerMilli = 1000.0

// MongoDBChecker implements the Checker interface for MongoDB health checks.
type MongoDBChecker struct{}

// Type returns the check type identifier.
func (c *MongoDBChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeMongoDB
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *MongoDBChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
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

	clientOpts := clientOptions(cfg, timeout)

	// Tunneled: dial through the bastion. mongo-driver's ContextDialer matches
	// checkerdef.ContextDialer, and the driver hands it the raw host:port (no
	// local resolution), so the bastion resolves the hostname. Untunneled, no
	// dialer is set and the driver uses its default net.Dialer byte-for-byte.
	tunneled := false
	if cfg.Socket != "" {
		// Unix socket (spec 2026-11-24-04): the shared dialer applies every
		// gate; Direct stops the driver discovering other members by hostname.
		clientOpts.SetDialer(checkerdef.LocalSocketDialer(ctx, cfg.Socket, timeout)).SetDirect(true)
	} else if dialer := checkerdef.TunnelDialerFrom(ctx); dialer != nil {
		clientOpts.SetDialer(dialer)

		tunneled = true
	} else if checkerdef.EgressEnforcing(ctx) {
		// Egress guard (spec 2026-09-25-19). The driver layers TLS above this
		// dialer itself, so only the TCP connect changes.
		clientOpts.SetDialer(checkerdef.GuardedNetDialer(ctx, &net.Dialer{Timeout: timeout}))
	}

	client, err := mongo.Connect(clientOpts)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   map[string]any{checkerdef.OutputKeyError: "failed to create client: " + err.Error()},
		}, nil
	}

	defer func() { _ = client.Disconnect(ctx) }()

	return c.ping(ctx, client, cfg, tunneled, start)
}

// socketPlaceholderHost is the host of a socket check: the checker's dialer
// ignores it and dials the socket through the shared gates.
const socketPlaceholderHost = "localhost"

// authSourceDefault is the database the credentials authenticate against.
const authSourceDefault = "admin"

// clientOptions builds the driver options from the structured config, never
// from a URI string (GHSA-38m7-jhhr-79xx): no config value reaches the
// driver's URI parser, so none can move the host or add a URI option
// (authMechanism, authMechanismProperties, appname, tlsCAFile...).
//
// The auth mechanism is left empty so the server negotiates SCRAM, and
// AuthMechanismProperties is never set: no config key may select OIDC, AWS,
// X.509 or GSSAPI.
func clientOptions(cfg *MongoDBConfig, timeout time.Duration) *options.ClientOptions {
	host := socketPlaceholderHost
	if cfg.Socket == "" {
		port := cfg.Port
		if port == 0 {
			port = defaultPort
		}

		host = net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	}

	opts := options.Client().
		SetHosts([]string{host}).
		SetConnectTimeout(timeout).
		SetTimeout(timeout)

	if cfg.Username != "" {
		// AuthSource stays "admin", what the former URI (which never carried a
		// database path) resolved to: a monitoring user created in admin keeps
		// authenticating when the check pings another database.
		opts.SetAuth(options.Credential{
			AuthSource:  authSourceDefault,
			Username:    cfg.Username,
			Password:    cfg.Password,
			PasswordSet: cfg.Password != "",
		})
	}

	return opts
}

// ping runs the MongoDB probe and builds the result.
//
// The error return is always nil today. It is kept so this matches the shape
// every other checker's ping helper has, and so a future failure path does not
// have to change the signature at every call site.
//
// Do NOT re-add a `//nolint:unparam` waiver here. golangci-lint v2.13.1 — the
// version CI pins (.github/workflows/ci.yml) — no longer reports unparam for
// this signature, so the waiver becomes an unused directive and nolintlint
// fails the build. Older local installs (v2.12.0 and earlier) DO still report
// unparam, so `make lint-back` can disagree with CI here: that means the local
// golangci-lint is stale, not that the waiver is missing. Upgrade it to match
// the pinned version rather than restoring the directive — commit dbbe08e93
// restored it for exactly this reason and broke CI.
func (c *MongoDBChecker) ping(
	ctx context.Context,
	client *mongo.Client,
	cfg *MongoDBConfig,
	tunneled bool,
	start time.Time,
) (*checkerdef.Result, error) {
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
			Output:   map[string]any{checkerdef.OutputKeyError: "ping command returned non-ok status"},
		}, nil
	}

	output := map[string]any{
		"host":   cfg.Host,
		"port":   port,
		"result": "ok",
	}

	if cfg.Socket != "" {
		output = map[string]any{"socket": cfg.Socket, "result": "ok"}
	}

	if tunneled {
		output["tunneled"] = true
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics: map[string]any{
			"ping_time_ms":  durationMs(time.Since(pingStart)),
			"total_time_ms": durationMs(time.Since(start)),
		},
		Output: output,
	}, nil
}

func handlePingError(ctx context.Context, err error, start time.Time) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Output:   map[string]any{checkerdef.OutputKeyError: "connection timeout"},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output:   map[string]any{checkerdef.OutputKeyError: "ping failed: " + err.Error()},
	}
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
