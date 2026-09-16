// Package healthcheck implements the local liveness probe used by the
// `solidping healthcheck` CLI subcommand. The shipped runtime image is
// distroless (no shell, no curl — see Dockerfile), so a Docker/Kubernetes
// HEALTHCHECK/livenessProbe cannot exec an arbitrary command inside the
// container; this subcommand exists specifically to be that command.
package healthcheck

import (
	"context"
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

// URLFromListen derives the loopback health-check URL from a server listen
// address such as ":4000", "0.0.0.0:4000" or "localhost:4000" (the shape of
// config.Config.Server.Listen / SP_SERVER_LISTEN). Only the port is used —
// the probe always targets 127.0.0.1 since it runs inside the same
// container/process group as the server it checks.
func URLFromListen(listen string) string {
	port := listen

	if _, p, err := net.SplitHostPort(listen); err == nil {
		port = p
	} else {
		port = strings.TrimPrefix(listen, ":")
	}

	return "http://127.0.0.1:" + port + healthPath
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
		return fmt.Errorf("healthcheck returned status %d", resp.StatusCode)
	}

	return nil
}
