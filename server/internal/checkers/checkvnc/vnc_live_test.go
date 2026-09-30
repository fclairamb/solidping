//go:build slowtests

// This file is behind the `slowtests` build tag because it starts a real VNC
// server (Xvfb + x11vnc) through Docker (testcontainers) and logs in to it.
// Excluded from `make test` and from both per-PR CI jobs; runs in the nightly
// `slowtests` workflow or via `make test-slow`. See wiki/testing/test-layers.md.

package checkvnc

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	liveVNCPassword = "secret"
	liveVNCWidth    = 800
	liveVNCHeight   = 600
)

// liveVNCImage is the base image the x11vnc server is installed into at
// start. Overridable so the nightly workflow can pin a mirror.
func liveVNCImage() string {
	if v := os.Getenv("TEST_VNC_BASE_IMAGE"); v != "" {
		return v
	}

	return "alpine:3.20"
}

// startX11VNC starts Xvfb + x11vnc (VNC auth, shared) and returns host, port.
// Skips (rather than fails) when Docker is unavailable.
func startX11VNC(t *testing.T) (string, int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	script := "apk add --no-cache xvfb x11vnc xterm font-misc-misc >/dev/null && " +
		"(Xvfb :0 -screen 0 " + strconv.Itoa(liveVNCWidth) + "x" + strconv.Itoa(liveVNCHeight) + "x24 &) && " +
		"sleep 1 && (DISPLAY=:0 xterm -geometry 80x24+0+0 &) && sleep 1 && " +
		"exec x11vnc -display :0 -forever -shared -passwd " + liveVNCPassword + " -rfbport 5900"

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        liveVNCImage(),
			Cmd:          []string{"sh", "-c", script},
			ExposedPorts: []string{"5900/tcp"},
			WaitingFor:   wait.ForListeningPort("5900/tcp").WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("vnc container unavailable (no Docker or image not pulled): %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })

	host, err := ctr.Host(ctx)
	require.NoError(t, err)

	mapped, err := ctr.MappedPort(ctx, "5900/tcp")
	require.NoError(t, err)

	port, err := strconv.Atoi(mapped.Port())
	require.NoError(t, err)

	return host, port
}

// TestLiveVNCScreenshot logs in with the password and captures one frame:
// a non-empty PNG of the desktop's size.
//
//nolint:paralleltest // testcontainer lifecycle
func TestLiveVNCScreenshot(t *testing.T) {
	r := require.New(t)
	host, port := startX11VNC(t)

	cfg := &VNCConfig{Host: host, Port: port, Password: liveVNCPassword, Screenshot: true, Timeout: 30 * time.Second}
	r.NoError(cfg.Validate())

	result, err := (&VNCChecker{}).Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Equal(liveVNCWidth, result.Output["width"])
	r.Equal(liveVNCHeight, result.Output["height"])
	r.NotNil(result.Diagnostics)
	r.NotNil(result.Diagnostics.Screenshot, result.Diagnostics.ScreenshotError)

	img, err := png.Decode(bytes.NewReader(result.Diagnostics.Screenshot.Image))
	r.NoError(err)
	r.Equal(liveVNCWidth, img.Bounds().Dx())
	r.Equal(liveVNCHeight, img.Bounds().Dy())

	// Wrong password against the same server: AUTH_FAILED, not up.
	bad := &VNCConfig{Host: host, Port: port, Password: "wrong", Timeout: 30 * time.Second}
	r.NoError(bad.Validate())

	badResult, err := (&VNCChecker{}).Execute(context.Background(), bad)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, badResult.Status, badResult.Output)
}

// TestLiveVNCScriptedSession drives the real x11vnc through the session API:
// type into the xterm and the terminal region's hash changes.
//
//nolint:paralleltest // testcontainer lifecycle
func TestLiveVNCScriptedSession(t *testing.T) {
	r := require.New(t)
	host, port := startX11VNC(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, err := OpenVNCSession(ctx, &VNCConfig{
		Host: host, Port: port, Password: liveVNCPassword, Timeout: 30 * time.Second,
	}, nil)
	r.NoError(err)

	defer session.End()

	r.NoError(session.WaitForStable(ctx, time.Second))

	before, err := session.RegionHash(0, 0, 400, 200)
	r.NoError(err)

	r.NoError(session.Click(100, 100))
	r.NoError(session.Type("echo scripted\n"))
	r.NoError(session.WaitForChange(ctx, 10*time.Second))
	r.NoError(session.WaitForStable(ctx, time.Second))

	after, err := session.RegionHash(0, 0, 400, 200)
	r.NoError(err)
	r.NotEqual(before, after, "typing into the xterm must change the terminal region")
}
