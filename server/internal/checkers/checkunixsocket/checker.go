// Package checkunixsocket checks a service that listens on a Unix domain
// socket: a bare connect, a raw send/expect exchange (the one `tcp` runs), or
// an HTTP GET over the socket.
//
// A socket path only means something on the host that runs the check, so the
// type never runs through a tunnel, and an enforcing egress guard (a SaaS
// shared worker) refuses it before any dial: such a worker must never become a
// probe of its own host's sockets.
package checkunixsocket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkunixsocket/config"
	"github.com/fclairamb/solidping/server/internal/egress"
)

const (
	defaultTimeout       = 5 * time.Second
	microsecondsPerMilli = 1000.0
	maxBodyRead          = 64 * 1024
	maxBodyOutput        = 1024
	httpStatusOKMin      = 200
	httpStatusOKMax      = 399

	// OutputKeyPath is the socket path the check dialed.
	OutputKeyPath = "path"
	// OutputKeyErrno is the symbolic errno of a failed dial (ENOENT, ECONNREFUSED, EACCES...).
	OutputKeyErrno = "errno"
	// OutputKeyMode is the mode the check ran in: connect, raw or http.
	OutputKeyMode = "mode"

	metricConnectTime  = "connect_time_ms"
	metricResponseTime = "response_time_ms"
	metricStatusCode   = "status_code"
)

// ErrTunnelNotSupported is returned when a unix_socket check runs through a
// tunnel: a socket path means nothing on the far side of a bastion.
var ErrTunnelNotSupported = errors.New("unix_socket checks cannot run through a tunnel")

// UnixSocketChecker implements the Checker interface for Unix domain sockets.
type UnixSocketChecker struct{}

// Type returns the check type identifier.
func (c *UnixSocketChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeUnixSocket
}

// Validate checks the configuration.
func (c *UnixSocketChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute dials the socket and runs the configured mode.
func (c *UnixSocketChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*UnixSocketConfig](config)
	if err != nil {
		return nil, err
	}

	start := time.Now()

	if refusal := refuse(ctx, cfg.Path); refusal != nil {
		return finish(&checkerdef.Result{
			Status: checkerdef.StatusError,
			Output: map[string]any{checkerdef.OutputKeyError: refusal.Error()},
		}, cfg, start), nil
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var result *checkerdef.Result
	if cfg.IsHTTPMode() {
		result = c.executeHTTP(ctxWithTimeout, cfg, timeout)
	} else {
		result = c.executeRaw(ctxWithTimeout, cfg, timeout)
	}

	return finish(result, cfg, start), nil
}

// refuse returns the reason this execution must not dial at all, or nil.
func refuse(ctx context.Context, path string) error {
	if checkerdef.TunnelDialerFrom(ctx) != nil {
		return ErrTunnelNotSupported
	}

	if egress.FromContext(ctx).Enforcing() {
		denied := &egress.DeniedError{Host: "unix:" + path}
		egress.RecordDenial(ctx, denied)

		return denied
	}

	return nil
}

func finish(result *checkerdef.Result, cfg *UnixSocketConfig, start time.Time) *checkerdef.Result {
	result.Duration = time.Since(start)

	if result.Output == nil {
		result.Output = map[string]any{}
	}

	result.Output[OutputKeyPath] = cfg.Path

	switch {
	case cfg.IsHTTPMode():
		result.Output[OutputKeyMode] = "http"
	case cfg.SendData != "" || cfg.ExpectData != "" || cfg.ExpectPattern != "":
		result.Output[OutputKeyMode] = "raw"
	default:
		result.Output[OutputKeyMode] = "connect"
	}

	return result
}

// dial opens the socket, returning the connection and its connect time, or
// the failure result.
func dial(ctx context.Context, path string) (net.Conn, time.Duration, *checkerdef.Result) {
	connectStart := time.Now()

	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, 0, dialFailure(ctx, err)
	}

	return conn, time.Since(connectStart), nil
}

// dialFailure turns a dial error into a result naming the errno, so the user
// can tell a missing socket file from a dead service or a permission problem.
func dialFailure(ctx context.Context, err error) *checkerdef.Result {
	output := map[string]any{}

	if ctx.Err() != nil {
		output[checkerdef.OutputKeyError] = "connection timeout"

		return &checkerdef.Result{Status: checkerdef.StatusTimeout, Output: output}
	}

	name, hint := describeErrno(err)
	if name != "" {
		output[OutputKeyErrno] = name
		output[checkerdef.OutputKeyError] = fmt.Sprintf("%s (%s): %v", hint, name, err)
	} else {
		output[checkerdef.OutputKeyError] = fmt.Sprintf("connection failed: %v", err)
	}

	return &checkerdef.Result{Status: checkerdef.StatusDown, Output: output}
}

func describeErrno(err error) (string, string) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return "", ""
	}

	switch {
	case errors.Is(errno, syscall.ENOENT):
		return "ENOENT", "socket file not found"
	case errors.Is(errno, syscall.ECONNREFUSED):
		return "ECONNREFUSED", "nothing is listening on the socket"
	case errors.Is(errno, syscall.EACCES), errors.Is(errno, syscall.EPERM):
		return "EACCES", "permission denied on the socket"
	case errors.Is(errno, syscall.ENOTSOCK):
		return "ENOTSOCK", "the path is not a socket"
	default:
		return fmt.Sprintf("errno %d", int(errno)), "connection failed"
	}
}

func connectMetric(connectTime time.Duration) float64 {
	return float64(connectTime.Microseconds()) / microsecondsPerMilli
}

// executeRaw runs the connect-only or the raw send/expect mode.
func (c *UnixSocketChecker) executeRaw(
	ctx context.Context, cfg *UnixSocketConfig, timeout time.Duration,
) *checkerdef.Result {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(timeout)
	}

	exchange, err := checkerdef.NewExchange(
		cfg.SendData, cfg.SendEncoding,
		cfg.ExpectData, cfg.ExpectEncoding,
		cfg.ExpectPattern, timeout, deadline,
	)
	if err != nil {
		return &checkerdef.Result{
			Status: checkerdef.StatusError,
			Output: map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("invalid payload configuration: %v", err),
			},
		}
	}

	conn, connectTime, failure := dial(ctx, cfg.Path)
	if failure != nil {
		return failure
	}

	defer func() { _ = conn.Close() }()

	metrics := map[string]any{metricConnectTime: connectMetric(connectTime)}
	output := map[string]any{}

	if result := exchange.Run(conn, metrics, output); result != nil {
		return result
	}

	return &checkerdef.Result{Status: checkerdef.StatusUp, Metrics: metrics, Output: output}
}

// executeHTTP sends `GET http_path` over the socket and asserts the status
// and, optionally, a body substring.
func (c *UnixSocketChecker) executeHTTP(
	ctx context.Context, cfg *UnixSocketConfig, _ time.Duration,
) *checkerdef.Result {
	var connectTime time.Duration

	transport := &http.Transport{
		DisableKeepAlives: true,
		Proxy:             nil,
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			connectStart := time.Now()

			conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", cfg.Path)
			connectTime = time.Since(connectStart)

			return conn, err
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		// A redirect would leave the socket for whatever host it names: report
		// the 3xx as is.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost"+cfg.HTTPPath, nil)
	if err != nil {
		return &checkerdef.Result{
			Status: checkerdef.StatusError,
			Output: map[string]any{checkerdef.OutputKeyError: fmt.Sprintf("invalid request: %v", err)},
		}
	}

	requestStart := time.Now()

	resp, err := client.Do(req)
	if err != nil {
		return httpFailure(ctx, err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	responseTime := time.Since(requestStart)

	metrics := map[string]any{
		metricConnectTime:  connectMetric(connectTime),
		metricResponseTime: float64(responseTime.Microseconds()) / microsecondsPerMilli,
		metricStatusCode:   resp.StatusCode,
	}
	output := map[string]any{
		checkerdef.OutputKeyStatusCode: resp.StatusCode,
	}

	if readErr != nil {
		if ctx.Err() != nil {
			output[checkerdef.OutputKeyError] = "timeout while reading the response body"

			return &checkerdef.Result{Status: checkerdef.StatusTimeout, Metrics: metrics, Output: output}
		}

		output[checkerdef.OutputKeyError] = fmt.Sprintf("failed to read the response body: %v", readErr)

		return &checkerdef.Result{Status: checkerdef.StatusDown, Metrics: metrics, Output: output}
	}

	if !statusMatches(cfg.ExpectedStatus, resp.StatusCode) {
		output[checkerdef.OutputKeyError] = statusMismatch(cfg.ExpectedStatus, resp.StatusCode)
		output["body"] = truncate(body)

		return &checkerdef.Result{Status: checkerdef.StatusDown, Metrics: metrics, Output: output}
	}

	if cfg.BodyExpect != "" && !strings.Contains(string(body), cfg.BodyExpect) {
		output[checkerdef.OutputKeyError] = fmt.Sprintf("response body does not contain %q", cfg.BodyExpect)
		output["body"] = truncate(body)

		return &checkerdef.Result{Status: checkerdef.StatusDown, Metrics: metrics, Output: output}
	}

	return &checkerdef.Result{Status: checkerdef.StatusUp, Metrics: metrics, Output: output}
}

func httpFailure(ctx context.Context, err error) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status: checkerdef.StatusTimeout,
			Output: map[string]any{checkerdef.OutputKeyError: "request timeout"},
		}
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return dialFailure(ctx, err)
	}

	return &checkerdef.Result{
		Status: checkerdef.StatusDown,
		Output: map[string]any{checkerdef.OutputKeyError: fmt.Sprintf("request failed: %v", err)},
	}
}

func statusMatches(expected, actual int) bool {
	if expected != 0 {
		return actual == expected
	}

	return actual >= httpStatusOKMin && actual <= httpStatusOKMax
}

func statusMismatch(expected, actual int) string {
	if expected != 0 {
		return fmt.Sprintf("expected status %d, got %d", expected, actual)
	}

	return fmt.Sprintf("expected a 2xx or 3xx status, got %d", actual)
}

func truncate(body []byte) string {
	if len(body) > maxBodyOutput {
		return string(body[:maxBodyOutput])
	}

	return string(body)
}
