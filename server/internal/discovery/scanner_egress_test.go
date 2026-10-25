package discovery

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// countingListener starts a TCP listener on 127.0.0.1 that accepts and closes
// every connection, counting them. It returns the port and the counter.
func countingListener(t *testing.T) (int, *atomic.Int64) {
	t.Helper()

	lc := net.ListenConfig{}

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	var accepted atomic.Int64

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			accepted.Add(1)

			_ = conn.Close()
		}
	}()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return tcpAddr.Port, &accepted
}

// Both scan entry points (CIDR Scan and the Freebox ScanHosts) skip a target
// the egress guard on the context refuses: no TCP connect reaches the
// listener and no host comes back. A permissive guard and no guard at all are
// the positive controls: the same listener is found with its port open.
func TestScanObeysEgressGuard(t *testing.T) {
	t.Parallel()

	scanners := map[string]func(ctx context.Context, port int) ([]DiscoveredHost, error){
		"Scan": func(ctx context.Context, port int) ([]DiscoveredHost, error) {
			res, err := Scan(ctx, &Config{CIDRs: []string{"127.0.0.1/32"}, Ports: []int{port}})
			if err != nil {
				return nil, err
			}

			return res.Hosts, nil
		},
		"ScanHosts": func(ctx context.Context, port int) ([]DiscoveredHost, error) {
			res, err := ScanHosts(ctx, []HostInput{{IP: net.ParseIP("127.0.0.1"), HostnameHint: "box"}},
				&Config{Ports: []int{port}})
			if err != nil {
				return nil, err
			}

			return res.Hosts, nil
		},
	}

	guards := []struct {
		name      string
		guard     *egress.Guard
		withGuard bool
		wantFound bool
	}{
		{name: "enforcing", guard: egress.New(false), withGuard: true, wantFound: false},
		{name: "permissive", guard: egress.New(true), withGuard: true, wantFound: true},
		{name: "no guard", withGuard: false, wantFound: true},
	}

	for scanName, scan := range scanners {
		for _, tc := range guards {
			t.Run(scanName+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				r := require.New(t)
				port, accepted := countingListener(t)

				ctx := t.Context()
				if tc.withGuard {
					ctx = egress.WithGuard(ctx, tc.guard)
				}

				hosts, err := scan(ctx, port)
				r.NoError(err)

				if !tc.wantFound {
					r.Empty(hosts, "a refused target must not be reported")
					r.Zero(accepted.Load(), "a refused target must not be dialed")

					return
				}

				r.Len(hosts, 1)
				r.Equal("127.0.0.1", hosts[0].IP)
				r.Equal([]int{port}, hosts[0].OpenPorts)
				// Accept runs on the listener goroutine, after the dial returned.
				r.Eventually(func() bool { return accepted.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
			})
		}
	}
}

// countingDockerAPI is a fake Docker API counting the requests it receives.
func countingDockerAPI(t *testing.T) (string, *atomic.Int64) {
	t.Helper()

	var requests atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)

		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("Api-Version", "1.45")
			w.WriteHeader(http.StatusOK)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"Id":"abc","Names":["/web"],"Image":"nginx","State":"running"}]`))
	}))
	t.Cleanup(srv.Close)

	return "tcp://" + strings.TrimPrefix(srv.URL, "http://"), &requests
}

// Under an enforcing guard ListContainers refuses a unix:// socket outright and
// dials a tcp:// endpoint through the guard, so a private daemon is never
// reached. The permissive guard is the control.
func TestListContainersObeysEgressGuard(t *testing.T) {
	t.Parallel()

	t.Run("enforcing tcp", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		endpoint, requests := countingDockerAPI(t)
		ctx := egress.WithGuard(t.Context(), egress.New(false))

		_, err := ListContainers(ctx, endpoint, 0)
		r.ErrorIs(err, egress.ErrDenied)
		r.Zero(requests.Load(), "the private daemon must receive no request")
	})

	t.Run("enforcing unix", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		ctx := egress.WithGuard(t.Context(), egress.New(false))

		_, err := ListContainers(ctx, "unix:///var/run/docker.sock", 0)

		var denied *egress.DeniedError
		r.ErrorAs(err, &denied)
	})

	t.Run("permissive control", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		endpoint, requests := countingDockerAPI(t)
		ctx := egress.WithGuard(t.Context(), egress.New(true))

		containers, err := ListContainers(ctx, endpoint, 0)
		r.NoError(err)
		r.Len(containers, 1)
		r.Positive(requests.Load())
	})
}
