package checkbrowser

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// countingServer is an HTTP server on addr counting the requests it gets.
func countingServer(t *testing.T, addr string) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var hits atomic.Int32

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", addr)
	if err != nil {
		t.Skipf("cannot listen on %s: %v", addr, err)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "hello")
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	return server, &hits
}

// connect sends a CONNECT for target through the proxy and returns the
// status line's code and the open connection.
func connect(t *testing.T, proxy *egressProxy, target string) (int, net.Conn, *bufio.Reader) {
	t.Helper()

	var dialer net.Dialer

	conn, err := dialer.DialContext(t.Context(), "tcp", proxy.listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	require.NoError(t, err)

	reader := bufio.NewReader(conn)

	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	require.NoError(t, err)

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
	}

	return resp.StatusCode, conn, reader
}

func TestEgressProxyRefusesPrivateTargetsBeforeConnecting(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	private, privateHits := countingServer(t, "127.0.0.1:0")
	public, publicHits := countingServer(t, "[::1]:0")

	ctx, rec := egress.WithRecorder(t.Context())
	guard := egress.New(false, egress.WithPublicOverride(net.ParseIP("::1")))

	var denials atomic.Int32

	proxy, err := startEgressProxy(ctx, guard, func(*egress.DeniedError) { denials.Add(1) })
	r.NoError(err)

	defer proxy.Close()

	privateHost := strings.TrimPrefix(private.URL, "http://")
	publicHost := strings.TrimPrefix(public.URL, "http://")

	// CONNECT (https, ws, wss) to a private address: refused, never dialed.
	code, _, _ := connect(t, proxy, privateHost)
	r.Equal(http.StatusForbidden, code)
	r.Equal(int32(1), denials.Load(), "the session is told")
	r.NotNil(rec.Denied(), "the execution's recorder too")

	// CONNECT to an allowed address: a working tunnel.
	code, conn, reader := connect(t, proxy, publicHost)
	r.Equal(http.StatusOK, code)

	_, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: "+publicHost+"\r\nConnection: close\r\n\r\n")
	r.NoError(err)

	resp, err := http.ReadResponse(reader, nil)
	r.NoError(err)

	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	r.Equal("hello", string(body))

	// Plain HTTP, the absolute-URI form Chrome sends to a proxy.
	proxyURL, err := url.Parse(proxy.URL())
	r.NoError(err)

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	plainPublic, err := http.NewRequestWithContext(t.Context(), http.MethodGet, public.URL+"/", nil)
	r.NoError(err)

	resp, err = client.Do(plainPublic)
	r.NoError(err)

	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("hello", string(body))

	plainPrivate, err := http.NewRequestWithContext(t.Context(), http.MethodGet, private.URL+"/", nil)
	r.NoError(err)

	resp, err = client.Do(plainPrivate)
	r.NoError(err)

	_ = resp.Body.Close()
	r.Equal(http.StatusForbidden, resp.StatusCode)

	r.Zero(privateHits.Load(), "the private server never got a request")
	r.Equal(int32(2), publicHits.Load())
}

func TestEgressProxyRefusesOriginFormRequests(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	proxy, err := startEgressProxy(context.Background(), egress.New(false), nil)
	r.NoError(err)

	defer proxy.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, proxy.URL()+"/", nil)
	r.NoError(err)

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)

	_ = resp.Body.Close()
	r.Equal(http.StatusBadRequest, resp.StatusCode, "the proxy is not a web server")
}
