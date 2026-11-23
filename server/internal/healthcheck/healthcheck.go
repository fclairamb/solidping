// Package healthcheck implements the local liveness probe used by the
// `solidping healthcheck` CLI subcommand. The shipped runtime image is
// distroless (no shell, no curl — see Dockerfile), so a Docker/Kubernetes
// HEALTHCHECK/livenessProbe cannot exec an arbitrary command inside the
// container; this subcommand exists specifically to be that command.
package healthcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout is the request timeout applied to the health probe.
const DefaultTimeout = 3 * time.Second

// healthPath is the server's health endpoint. It answers 200 when healthy
// and 503 during the graceful-shutdown window (server.go), which is exactly
// the signal an orchestrator wants from this probe.
const healthPath = "/api/mgmt/health"

// errUnhealthy wraps a non-200 response so Check's error is a static,
// wrapped sentinel (err113) rather than an ad-hoc dynamic one, while still
// carrying the actual status code via %w/%d formatting at the call site.
var errUnhealthy = errors.New("healthcheck: unexpected status")

// URLFromListen derives the loopback health-check URL from a server listen
// address such as ":4000", "0.0.0.0:4000" or "localhost:4000" (the shape of
// config.Config.Server.Listen / SP_SERVER_LISTEN). Only the port is used —
// the probe always targets 127.0.0.1 since it runs inside the same
// container/process group as the server it checks.
func URLFromListen(listen string) string {
	return "http://" + LoopbackAddr(listen) + healthPath
}

// LoopbackAddr maps a listen address such as ":4000" to "127.0.0.1:4000": the
// address the probe targets, and the one agent mode binds its health listener
// to.
func LoopbackAddr(listen string) string {
	var port string

	if _, p, err := net.SplitHostPort(listen); err == nil {
		port = p
	} else {
		port = strings.TrimPrefix(listen, ":")
	}

	return "127.0.0.1:" + port
}

// readHeaderTimeout bounds how long the agent health listener waits for a
// request's headers (gosec G112).
const readHeaderTimeout = 5 * time.Second

// Serve answers GET /api/mgmt/health on listener until ctx is canceled: 200
// while healthy() is true, 503 otherwise, 404 for any other path. Agent mode
// runs it on loopback (it has no HTTP server of its own), so the image's
// HEALTHCHECK reports whether the agent holds its link to the server (issue
// #995).
func Serve(ctx context.Context, listener net.Listener, healthy func() bool) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+healthPath, func(writer http.ResponseWriter, _ *http.Request) {
		if !healthy() {
			writer.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		writer.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}

	go func() {
		<-ctx.Done()

		_ = srv.Close()
	}()

	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("health listener: %w", err)
	}

	return nil
}

// Check performs a single GET against url and returns nil only when the
// response status is HTTP 200. Any transport error, timeout, or non-200
// status is returned as a descriptive error so the CLI can log it before
// exiting non-zero.
func Check(ctx context.Context, url string, timeout time.Duration) error {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build healthcheck request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %d", errUnhealthy, resp.StatusCode)
	}

	return nil
}
