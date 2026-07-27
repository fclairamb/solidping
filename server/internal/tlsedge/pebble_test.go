package tlsedge

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/miekg/dns"
)

// Pebble (https://github.com/letsencrypt/pebble) is Let's Encrypt's test ACME
// CA. It speaks the real protocol — account registration, orders, challenges,
// CSR, issuance — so driving certmagic against it proves the whole ACME path
// end to end without touching a public CA or a real rate limit.
//
// Validation is REAL here: PEBBLE_VA_ALWAYS_VALID is deliberately NOT set, so
// Pebble's validation authority actually connects back over the network to
// solve the challenge. Two pieces of plumbing make that reachable from inside a
// container:
//
//   - `-dnsserver` points Pebble at the in-process DNS server below, which
//     resolves the test hostname to the address the container reaches this
//     process on (`host.docker.internal`, mapped with the `host-gateway` extra
//     host so it works on plain Linux Docker as well as Docker Desktop);
//   - the CA's own config has `httpPort` / `tlsPort` rewritten to the ephemeral
//     ports the Edge under test binds, so Pebble's HTTP-01 validator hits the
//     Edge's `:80` challenge handler and its TLS-ALPN-01 validator hits the
//     Edge's `:443` listener.
//
// Consequence: a certificate can only be issued if the Edge's own solver wiring
// genuinely works. Break the challenge handler or drop "acme-tls/1" from the
// TLS config and the order fails, turning the test red.
const (
	pebbleImage       = "ghcr.io/letsencrypt/pebble:latest"
	pebbleDirPort     = "14000/tcp"
	pebbleMiniCAPath  = "test/certs/pebble.minica.pem"
	pebbleConfigPath  = "test/config/pebble-config.json"
	pebbleHostAlias   = "host.docker.internal"
	pebbleStartupWait = 45 * time.Second
	pebblePollEvery   = 250 * time.Millisecond
)

// pebbleCA is a running Pebble container.
type pebbleCA struct {
	DirectoryURL string
	Roots        *x509.CertPool

	cli         *client.Client
	containerID string
}

// Logs returns everything the CA has written so far. Pebble's validation
// authority logs every challenge attempt with the identifier, which is how the
// tests assert that a domain did (or did not) cause any CA-side work.
func (c *pebbleCA) Logs(ctx context.Context, t *testing.T) string {
	t.Helper()

	reader, err := c.cli.ContainerLogs(ctx, c.containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		t.Fatalf("cannot read pebble logs: %v", err)
	}

	defer func() { _ = reader.Close() }()

	// The container runs with a TTY, so the stream is raw rather than
	// stdcopy-multiplexed.
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("cannot read pebble logs: %v", err)
	}

	return string(out)
}

// startPebble boots a Pebble container wired to validate challenges against the
// given host ports, and returns its directory URL plus the root pool needed to
// talk to it. It t.Skips (never fails) when Docker or the image is unavailable,
// matching the embedded-postgres convention in this repo: a developer without
// Docker still gets a green run, CI with Docker gets the coverage.
func startPebble(ctx context.Context, t *testing.T, resolver *testDNS, httpPort, tlsPort string) *pebbleCA {
	t.Helper()

	cli := dockerClient(ctx, t)

	hostPort, err := freePort(ctx)
	if err != nil {
		t.Skipf("cannot reserve a host port: %v", err)
	}

	created, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: pebbleImage,
			// Skip only the artificial validation delay, so a test issuance takes
			// milliseconds instead of tens of seconds. PEBBLE_VA_ALWAYS_VALID is
			// deliberately absent: validation must be real.
			Env: []string{"PEBBLE_VA_NOSLEEP=1"},
			// Resolve every name through the in-process DNS server, so the
			// validator comes back to this test's listeners.
			Cmd:          []string{"-dnsserver", pebbleHostAlias + ":" + resolver.Port()},
			ExposedPorts: nat.PortSet{pebbleDirPort: struct{}{}},
			Tty:          true,
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				pebbleDirPort: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: hostPort}},
			},
			// host-gateway makes host.docker.internal resolvable inside the
			// container on Linux too, not only on Docker Desktop.
			ExtraHosts: []string{pebbleHostAlias + ":host-gateway"},
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

	if cfgErr := pointPebbleAtChallengePorts(ctx, cli, created.ID, httpPort, tlsPort); cfgErr != nil {
		t.Skipf("cannot rewrite the pebble config: %v", cfgErr)
	}

	if startErr := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); startErr != nil {
		t.Skipf("cannot start pebble container: %v", startErr)
	}

	// Docker only populates /etc/hosts once the container starts, and that is
	// where the address the container reaches this process on comes from.
	hostIP, err := containerHostAddress(ctx, cli, created.ID)
	if err != nil {
		t.Skipf("cannot determine the container's route back to the host: %v", err)
	}

	resolver.SetTarget(hostIP)
	t.Logf("pebble resolves test hostnames to %s (this host, as seen from the container)", hostIP)

	roots, err := readPebbleRoots(ctx, cli, created.ID)
	if err != nil {
		t.Skipf("cannot read pebble root certificate: %v", err)
	}

	directoryURL := "https://127.0.0.1:" + hostPort + "/dir"
	if waitErr := waitForACMEDirectory(ctx, directoryURL, roots); waitErr != nil {
		t.Skipf("pebble did not become ready: %v", waitErr)
	}

	return &pebbleCA{DirectoryURL: directoryURL, Roots: roots, cli: cli, containerID: created.ID}
}

// dockerClient returns a live Docker client with the Pebble image pulled,
// skipping the test when Docker is not usable here.
func dockerClient(ctx context.Context, t *testing.T) *client.Client {
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

	return cli
}

// pointPebbleAtChallengePorts rewrites the CA's own config so its validator
// connects to the ports the Edge under test binds instead of the standard 80 /
// 443, which a test process cannot take. Without it every validation would be
// attempted against a port nothing listens on and every order would fail.
func pointPebbleAtChallengePorts(
	ctx context.Context, cli *client.Client, containerID, httpPort, tlsPort string,
) error {
	raw, err := copyFileFromContainer(ctx, cli, containerID, pebbleConfigPath)
	if err != nil {
		return err
	}

	var cfg map[string]any
	if unmarshalErr := json.Unmarshal(raw, &cfg); unmarshalErr != nil {
		return fmt.Errorf("decode pebble config: %w", unmarshalErr)
	}

	section, ok := cfg["pebble"].(map[string]any)
	if !ok {
		return errPebbleConfigShape
	}

	httpPortNum, err := strconv.Atoi(httpPort)
	if err != nil {
		return fmt.Errorf("http port: %w", err)
	}

	tlsPortNum, err := strconv.Atoi(tlsPort)
	if err != nil {
		return fmt.Errorf("tls port: %w", err)
	}

	section["httpPort"] = httpPortNum
	section["tlsPort"] = tlsPortNum

	patched, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode pebble config: %w", err)
	}

	archive, err := tarFile(pebbleConfigPath, patched)
	if err != nil {
		return err
	}

	if copyErr := cli.CopyToContainer(ctx, containerID, "/", bytes.NewReader(archive),
		container.CopyToContainerOptions{}); copyErr != nil {
		return fmt.Errorf("write pebble config: %w", copyErr)
	}

	return nil
}

var errPebbleConfigShape = errors.New("unexpected pebble config shape")

// containerHostAddress returns the IP the container uses to reach this process,
// read from the /etc/hosts entry Docker writes for host.docker.internal. It
// falls back to the bridge gateway, which is the host on plain Linux Docker.
func containerHostAddress(ctx context.Context, cli *client.Client, containerID string) (net.IP, error) {
	if hosts, err := copyFileFromContainer(ctx, cli, containerID, "/etc/hosts"); err == nil {
		if ip := hostAliasFromHostsFile(string(hosts)); ip != nil {
			return ip, nil
		}
	}

	inspected, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect container: %w", err)
	}

	if ip := net.ParseIP(inspected.NetworkSettings.Gateway); ip != nil {
		return ip, nil
	}

	return nil, errNoHostRoute
}

// hostAliasFromHostsFile finds the address mapped to host.docker.internal in an
// /etc/hosts file.
func hostAliasFromHostsFile(hosts string) net.IP {
	for _, line := range strings.Split(hosts, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		for _, name := range fields[1:] {
			if name != pebbleHostAlias {
				continue
			}

			if ip := net.ParseIP(fields[0]); ip != nil {
				return ip
			}
		}
	}

	return nil
}

var errNoHostRoute = errors.New("no route from the container back to the host")

// testDNS is a minimal DNS server that answers every A query with one address.
// Pebble is pointed at it so the hostname under test resolves to this process
// rather than to anything real. Non-A queries (AAAA, CAA, TXT) get an empty
// NOERROR, which is what "no such record" looks like — in particular it means
// no CAA record forbids issuance.
type testDNS struct {
	port string

	mu     sync.RWMutex
	target net.IP
}

// startTestDNS binds an ephemeral port and serves until the test ends. It
// answers on BOTH UDP and TCP on that port: Pebble's resolver client uses TCP,
// while ordinary clients use UDP, and a port that only answers one of them
// looks like "connection refused" rather than a DNS failure.
func startTestDNS(t *testing.T) *testDNS {
	t.Helper()

	packetConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Skipf("cannot bind a DNS port: %v", err)
	}

	addr, ok := packetConn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("unexpected DNS listener address type %T", packetConn.LocalAddr())
	}

	resolver := &testDNS{port: strconv.Itoa(addr.Port)}
	handler := dns.HandlerFunc(resolver.handle)

	streamListener, err := net.Listen("tcp", ":"+resolver.port)
	if err != nil {
		_ = packetConn.Close()
		t.Skipf("cannot bind the DNS port for TCP: %v", err)
	}

	servers := []*dns.Server{
		{PacketConn: packetConn, Handler: handler},
		{Listener: streamListener, Handler: handler},
	}

	for _, server := range servers {
		go func() {
			_ = server.ActivateAndServe()
		}()
	}

	t.Cleanup(func() {
		for _, server := range servers {
			_ = server.Shutdown()
		}
	})

	return resolver
}

// Port is the UDP port the server listens on.
func (d *testDNS) Port() string { return d.port }

// SetTarget sets the address every A query resolves to.
func (d *testDNS) SetTarget(ip net.IP) {
	d.mu.Lock()
	d.target = ip
	d.mu.Unlock()
}

func (d *testDNS) currentTarget() net.IP {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.target
}

func (d *testDNS) handle(writer dns.ResponseWriter, req *dns.Msg) {
	msg := new(dns.Msg)
	msg.SetReply(req)
	msg.Authoritative = true

	target := d.currentTarget()

	for _, question := range req.Question {
		if question.Qtype != dns.TypeA || target == nil {
			continue
		}

		msg.Answer = append(msg.Answer, &dns.A{
			Hdr: dns.RR_Header{
				Name:   question.Name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    1,
			},
			A: target,
		})
	}

	_ = writer.WriteMsg(msg)
}

// readPebbleRoots copies Pebble's self-signed minica certificate out of the
// container so the ACME client can trust the test CA's API endpoint.
func readPebbleRoots(ctx context.Context, cli *client.Client, containerID string) (*x509.CertPool, error) {
	pemBytes, err := copyFileFromContainer(ctx, cli, containerID, pebbleMiniCAPath)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errPebbleRootMissing
	}

	return pool, nil
}

var errPebbleRootMissing = errors.New("pebble root certificate not found in container")

// copyFileFromContainer reads a single regular file out of a container.
func copyFileFromContainer(ctx context.Context, cli *client.Client, containerID, path string) ([]byte, error) {
	reader, _, err := cli.CopyFromContainer(ctx, containerID, path)
	if err != nil {
		return nil, fmt.Errorf("copy %s: %w", path, err)
	}

	defer func() { _ = reader.Close() }()

	tarReader := tar.NewReader(reader)

	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			return nil, fmt.Errorf("%w: %s", errContainerFileMissing, path)
		}

		if nextErr != nil {
			return nil, fmt.Errorf("read tar: %w", nextErr)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		content, readErr := io.ReadAll(tarReader)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", path, readErr)
		}

		return content, nil
	}
}

var errContainerFileMissing = errors.New("file not found in container")

// tarFile wraps one file in a tar archive, which is what CopyToContainer takes.
func tarFile(name string, content []byte) ([]byte, error) {
	var buf bytes.Buffer

	writer := tar.NewWriter(&buf)
	if err := writer.WriteHeader(&tar.Header{
		Name:     strings.TrimPrefix(name, "/"),
		Mode:     0o644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return nil, fmt.Errorf("tar header: %w", err)
	}

	if _, err := writer.Write(content); err != nil {
		return nil, fmt.Errorf("tar body: %w", err)
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("tar close: %w", err)
	}

	return buf.Bytes(), nil
}

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
