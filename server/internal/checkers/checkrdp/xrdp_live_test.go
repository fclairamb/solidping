//go:build slowtests

// This file is behind the `slowtests` build tag because it starts a real xrdp
// container through Docker (testcontainers) and drives real logons against
// it. Excluded from `make test` and from both per-PR CI jobs; runs in the
// nightly `slowtests` workflow or via `make test-slow`. Windows Server cannot
// run in CI at all — the manual procedure for it lives in the wiki spike page
// (wiki/research/rdp-go-client-spike.md). See wiki/testing/test-layers.md.

package checkrdp

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrdp/config"
)

// Suite inputs, overridable from the environment so the nightly workflow and
// a manual run against a pinned image both work without code edits.
//
//nolint:gochecknoglobals // suite inputs, env-overridable
var (
	xrdpImage = envOr("TEST_XRDP_IMAGE", "dclong/xrdp:latest")
	xrdpUser  = envOr("TEST_XRDP_USER", "ubuntu")
	xrdpPass  = envOr("TEST_XRDP_PASSWORD", "password")
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

// startXRDP starts one xrdp container and returns the mapped host and port.
// Skips (rather than fails) when Docker is unavailable or the image is not
// pulled — the nightly workflow pulls it first.
func startXRDP(t *testing.T) (string, int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        xrdpImage,
			ExposedPorts: []string{"3389/tcp"},
			WaitingFor:   wait.ForListeningPort("3389/tcp"),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("xrdp container unavailable (no Docker or image not pulled): %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })

	host, err := ctr.Host(ctx)
	require.NoError(t, err)

	mapped, err := ctr.MappedPort(ctx, "3389/tcp")
	require.NoError(t, err)

	port, err := strconv.Atoi(mapped.Port())
	require.NoError(t, err)

	return host, port
}

// TestXRDPAuthenticatedLogonIsUp covers the core authenticated loop: a logon
// with the right credentials settles the desktop and earns an up.
//
//nolint:paralleltest // testcontainer lifecycle
func TestXRDPAuthenticatedLogonIsUp(t *testing.T) {
	r := require.New(t)
	host, port := startXRDP(t)

	cfg := &checkconfig.RDPConfig{
		Host:       host,
		Port:       port,
		Username:   xrdpUser,
		Password:   xrdpPass,
		Screenshot: true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := (&RDPChecker{}).Execute(ctx, cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
}

// TestXRDPAuthFailureIsRejected proves the distinct failure code: a wrong
// password is Down with failure_code auth_rejected, not timeout.
//
//nolint:paralleltest // testcontainer lifecycle
func TestXRDPAuthFailureIsRejected(t *testing.T) {
	r := require.New(t)
	host, port := startXRDP(t)

	cfg := &checkconfig.RDPConfig{
		Host:     host,
		Port:     port,
		Username: xrdpUser,
		Password: "wrong-password",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := (&RDPChecker{}).Execute(ctx, cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status, result.Output)
	r.Equal("auth_rejected", result.Output["failure_code"])
}

// TestXRDPLogoffVsDisconnect proves the two session-end modes are distinct in
// the output — the detail the docs promise.
//
//nolint:paralleltest // one container for both modes
func TestXRDPLogoffVsDisconnect(t *testing.T) {
	r := require.New(t)
	host, port := startXRDP(t)

	logoffCfg := &checkconfig.RDPConfig{
		Host:       host,
		Port:       port,
		Username:   xrdpUser,
		Password:   xrdpPass,
		EndSession: checkconfig.EndSessionLogoff,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	logoffResult, err := (&RDPChecker{}).Execute(ctx, logoffCfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, logoffResult.Status, logoffResult.Output)
	r.Equal("logoff", logoffResult.Output["end_session"])

	discCfg := &checkconfig.RDPConfig{
		Host:       host,
		Port:       port,
		Username:   xrdpUser,
		Password:   xrdpPass,
		EndSession: checkconfig.EndSessionDisconnect,
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel2()
	discResult, err := (&RDPChecker{}).Execute(ctx2, discCfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, discResult.Status)
	r.Equal("disconnect", discResult.Output["end_session"])
}

// TestRDPTunnelDial proves the tunnel half of the contract: the checker handed
// a pre-dialed connection (what a JS tunnel carries) reaches the same target.
// The conn here is a plain dial the test made — the seam is what both the
// check and the JS object consume.
//
//nolint:paralleltest // testcontainer lifecycle
func TestRDPTunnelDial(t *testing.T) {
	r := require.New(t)
	host, port := startXRDP(t)

	address := net.JoinHostPort(host, strconv.Itoa(port))

	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
	r.NoError(err, "the container's RDP port must be dialable before the logon test")

	cfg := &checkconfig.RDPConfig{
		Host:     host,
		Port:     port,
		Username: xrdpUser,
		Password: xrdpPass,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	checker := &RDPChecker{
		preDialedConn: func(_ context.Context, _ *checkconfig.RDPConfig) (net.Conn, error) {
			return conn, nil
		},
	}

	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
}

// TestXRDPInputAndReattach drives click+type against the live desktop (the
// input verbs the JS `rdp` object exposes), then proves the disconnect mode's
// reattach promise: the NEXT run succeeds against the session the previous run
// left behind.
//
//nolint:paralleltest // one container for the whole flow
func TestXRDPInputAndReattach(t *testing.T) {
	r := require.New(t)
	host, port := startXRDP(t)

	cfg := &checkconfig.RDPConfig{
		Host:       host,
		Port:       port,
		Username:   xrdpUser,
		Password:   xrdpPass,
		EndSession: checkconfig.EndSessionDisconnect,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Run 1: logon, drive the desktop, disconnect (session left running).
	first, err := (&RDPChecker{}).Execute(ctx, cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, first.Status, first.Output)

	session, err := OpenRDPSession(ctx, cfg, 0, 0, nil)
	r.NoError(err, "the second run must reattach to the session the first left")

	r.NoError(session.WaitForStable(ctx, stableQuiet))
	r.NoError(session.Click(640, 400))
	r.NoError(session.Type("hello"))
	r.NoError(session.Key("enter"))

	color, err := session.Pixel(10, 10)
	r.NoError(err, "a settled desktop answers pixel reads")
	_ = color

	_, err = session.RegionHash(0, 0, 1280, 800)
	r.NoError(err, "a settled desktop answers region hashes")

	// Best-effort by contract: the container's teardown ends everything.
	_ = session.EndLogoff()
}
