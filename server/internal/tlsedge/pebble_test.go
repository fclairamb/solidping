package tlsedge

import (
	"archive/tar"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// Pebble (https://github.com/letsencrypt/pebble) is Let's Encrypt's test ACME
// CA. It speaks the real protocol — account registration, orders, challenges,
// CSR, issuance — so driving certmagic against it proves the whole ACME path
// end to end without touching a public CA or a real rate limit.
//
// PEBBLE_VA_ALWAYS_VALID short-circuits only the *validation* network callback
// (the CA would otherwise have to reach the test process from inside Docker on
// a public name). Everything the SolidPing code owns — the decision gate,
// on-demand issuance during a handshake, DB-backed storage, reuse on the second
// handshake — is exercised for real.
const (
	pebbleImage       = "ghcr.io/letsencrypt/pebble:latest"
	pebbleDirPort     = "14000/tcp"
	pebbleMiniCAPath  = "test/certs/pebble.minica.pem"
	pebbleStartupWait = 45 * time.Second
	pebblePollEvery   = 250 * time.Millisecond
)

// pebbleCA is a running Pebble container.
type pebbleCA struct {
	DirectoryURL string
	Roots        *x509.CertPool
}

// startPebble boots a Pebble container and returns its directory URL plus the
// root pool needed to talk to it. It t.Skips (never fails) when Docker or the
// image is unavailable, matching the embedded-postgres convention in this repo:
// a developer without Docker still gets a green run, CI with Docker gets the
// coverage.
func startPebble(ctx context.Context, t *testing.T) *pebbleCA {
	t.Helper()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}

	t.Cleanup(func() { _ = cli.Close() })

	if _, pingErr := cli.Ping(ctx); pingErr != nil {
		t.Skipf("docker daemon unavailable: %v", pingErr)
	}

	pullReader, err := cli.ImagePull(ctx, pebbleImage, image.PullOptions{})
	if err != nil {
		t.Skipf("cannot pull %s: %v", pebbleImage, err)
	}

	_, _ = io.Copy(io.Discard, pullReader)
	_ = pullReader.Close()

	hostPort, err := freePort(ctx)
	if err != nil {
		t.Skipf("cannot reserve a host port: %v", err)
	}

	created, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: pebbleImage,
			Env: []string{
				// Skip the outbound validation callback (see the comment above)
				// and the artificial validation delay, so a test issuance takes
				// milliseconds instead of tens of seconds.
				"PEBBLE_VA_ALWAYS_VALID=1",
				"PEBBLE_VA_NOSLEEP=1",
			},
			ExposedPorts: nat.PortSet{pebbleDirPort: struct{}{}},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				pebbleDirPort: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: hostPort}},
			},
			AutoRemove: true,
		}, nil, nil, "")
	if err != nil {
		t.Skipf("cannot create pebble container: %v", err)
	}

	// contextcheck: cleanup must run on a fresh context — the test's context is
	// already canceled by the time t.Cleanup fires.
	t.Cleanup(func() { //nolint:contextcheck
		removeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = cli.ContainerRemove(removeCtx, created.ID, container.RemoveOptions{Force: true})
	})

	if startErr := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); startErr != nil {
		t.Skipf("cannot start pebble container: %v", startErr)
	}

	roots, err := readPebbleRoots(ctx, cli, created.ID)
	if err != nil {
		t.Skipf("cannot read pebble root certificate: %v", err)
	}

	directoryURL := "https://127.0.0.1:" + hostPort + "/dir"
	if waitErr := waitForACMEDirectory(ctx, directoryURL, roots); waitErr != nil {
		t.Skipf("pebble did not become ready: %v", waitErr)
	}

	return &pebbleCA{DirectoryURL: directoryURL, Roots: roots}
}

// readPebbleRoots copies Pebble's self-signed minica certificate out of the
// container so the ACME client can trust the test CA's API endpoint.
func readPebbleRoots(ctx context.Context, cli *client.Client, containerID string) (*x509.CertPool, error) {
	reader, _, err := cli.CopyFromContainer(ctx, containerID, pebbleMiniCAPath)
	if err != nil {
		return nil, fmt.Errorf("copy %s: %w", pebbleMiniCAPath, err)
	}
	defer func() { _ = reader.Close() }()

	tarReader := tar.NewReader(reader)

	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			return nil, errPebbleRootMissing
		}

		if nextErr != nil {
			return nil, fmt.Errorf("read tar: %w", nextErr)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		pemBytes, readErr := io.ReadAll(tarReader)
		if readErr != nil {
			return nil, fmt.Errorf("read pem: %w", readErr)
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, errPebbleRootMissing
		}

		return pool, nil
	}
}

var errPebbleRootMissing = errors.New("pebble root certificate not found in container")

// waitForACMEDirectory polls the CA's directory endpoint until it answers.
func waitForACMEDirectory(ctx context.Context, directoryURL string, roots *x509.CertPool) error {
	httpClient := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}},
	}

	deadline := time.Now().Add(pebbleStartupWait)

	var lastErr error

	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, directoryURL, nil)
		if err != nil {
			return err
		}

		resp, err := httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return nil
			}

			lastErr = fmt.Errorf("%w: status %d", errPebbleNotReady, resp.StatusCode)
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pebblePollEvery):
		}
	}

	if lastErr == nil {
		lastErr = errPebbleNotReady
	}

	return lastErr
}

var errPebbleNotReady = errors.New("pebble directory not ready")

// freePort reserves an ephemeral port and releases it, returning the number.
func freePort(ctx context.Context) (string, error) {
	var lc net.ListenConfig

	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}

	defer func() { _ = listener.Close() }()

	_, port, err := net.SplitHostPort(listener.Addr().String())

	return port, err
}
