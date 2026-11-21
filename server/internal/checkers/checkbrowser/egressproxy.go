package checkbrowser

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// egressProxy is the guarded HTTP/CONNECT proxy one browser session's Chrome
// is told to send ALL its traffic through under an enforcing egress policy
// (spec 2026-11-19-04).
//
// Chrome has its own network stack, so it cannot be handed the guard's
// dialer. Observing what it connected to (watchEgress) only poisons the
// session AFTER the fact: a WebSocket to an internal service, which has no
// same-origin barrier, still delivered its frames to the page. Routing every
// connection through this proxy moves the decision before the connection:
// the proxy resolves and dials with the guard, so a non-public destination is
// never contacted by what Chrome sends through it: navigations, subresources,
// fetch/XHR and WebSockets (Chrome tunnels ws:// and wss:// through CONNECT).
//
// WebRTC is the exception: its ICE checks are UDP and do not go through an
// HTTP proxy. It is handled apart (spec 2026-11-20-01): the Chrome this
// process starts runs with a WebRTC IP handling policy that forbids
// non-proxied UDP (egressExecFlags), and every enforcing session removes
// RTCPeerConnection from its pages (disableWebRTC).
//
// It listens on the loopback only, for the lifetime of one session.
type egressProxy struct {
	listener net.Listener
	server   *http.Server
	guard    *egress.Guard
	// recordCtx carries the execution's egress Recorder.
	recordCtx context.Context //nolint:containedctx // the proxy lives exactly as long as the execution it records for
	// onDenied is told about every refusal, to poison the session.
	onDenied func(*egress.DeniedError)
	forward  *httputil.ReverseProxy

	mu      sync.Mutex
	tunnels map[net.Conn]struct{}
	closed  bool
}

// egressProxyBypassList cancels Chrome's implicit proxy bypass of localhost
// and loopback addresses: without it a page could reach this node's own
// services directly, around the proxy.
const egressProxyBypassList = "<-loopback>"

// schemeHTTP is the only scheme the proxy forwards in absolute-URI form;
// everything else (https, ws, wss) arrives as a CONNECT tunnel.
const schemeHTTP = "http"

// egressProxyDialTimeout bounds the dial of one proxied connection.
const egressProxyDialTimeout = 30 * time.Second

// startEgressProxy starts a proxy on an ephemeral loopback port.
func startEgressProxy(
	recordCtx context.Context, guard *egress.Guard, onDenied func(*egress.DeniedError),
) (*egressProxy, error) {
	var lc net.ListenConfig

	listener, err := lc.Listen(recordCtx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	proxy := &egressProxy{
		listener:  listener,
		guard:     guard,
		recordCtx: recordCtx,
		onDenied:  onDenied,
		tunnels:   map[net.Conn]struct{}{},
	}

	transport := &http.Transport{
		Proxy:             nil, // never chain to an environment proxy
		DialContext:       proxy.dial,
		ForceAttemptHTTP2: false,
		TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxIdleConns:      defaultProxyMaxIdleConns,
		IdleConnTimeout:   defaultProxyIdleTimeout,
	}

	proxy.forward = &httputil.ReverseProxy{
		// The request already carries the absolute target URL (a proxy
		// request line): keep it, only the hop-by-hop headers go.
		Rewrite:   func(r *httputil.ProxyRequest) { r.Out.Host = r.In.Host },
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			proxy.refuse(w, err)
		},
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}

	proxy.server = &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: egressProxyDialTimeout,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}

	go func() { _ = proxy.server.Serve(listener) }()

	return proxy, nil
}

const (
	defaultProxyMaxIdleConns = 32
	defaultProxyIdleTimeout  = 30 * time.Second
)

// URL is the --proxy-server value pointing Chrome at this proxy.
func (p *egressProxy) URL() string {
	return "http://" + p.listener.Addr().String()
}

// host is the proxy's listening IP, for the resolver rules.
func (p *egressProxy) host() string {
	host, _, _ := net.SplitHostPort(p.listener.Addr().String())

	return host
}

// isProxyAddr reports whether a remote address Chrome reported for a response
// is this proxy: such a response went through the guard already.
func (p *egressProxy) isProxyAddr(ip net.IP, port int64) bool {
	if p == nil {
		return false
	}

	addr, ok := p.listener.Addr().(*net.TCPAddr)

	return ok && addr.IP.Equal(ip) && int64(addr.Port) == port
}

// Close stops the proxy and cuts every tunnel still open.
func (p *egressProxy) Close() {
	p.mu.Lock()
	p.closed = true

	for conn := range p.tunnels {
		_ = conn.Close()
	}

	p.tunnels = map[net.Conn]struct{}{}
	p.mu.Unlock()

	_ = p.server.Close()
}

// dial connects through the guard and reports a refusal.
func (p *egressProxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, egressProxyDialTimeout)
	defer cancel()

	conn, err := p.guard.DialContext(dialCtx, network, addr)
	if err != nil {
		var denied *egress.DeniedError
		if errors.As(err, &denied) {
			// The dial runs on the request's context; the Recorder lives on
			// the execution's.
			egress.RecordDenial(p.recordCtx, denied) //nolint:contextcheck // see above

			if p.onDenied != nil {
				p.onDenied(denied)
			}
		}

		return nil, err
	}

	return conn, nil
}

// refuse answers a request the proxy could not carry. A refusal by the
// policy is a 403 so Chrome fails the request instead of retrying it.
func (p *egressProxy) refuse(writer http.ResponseWriter, err error) {
	if errors.Is(err, egress.ErrDenied) {
		http.Error(writer, "denied by egress policy", http.StatusForbidden)

		return
	}

	http.Error(writer, "egress proxy: "+err.Error(), http.StatusBadGateway)
}

// ServeHTTP carries one proxied request: a CONNECT tunnel (https, ws, wss) or
// an absolute-URI plain-HTTP request.
func (p *egressProxy) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		p.tunnel(writer, req)

		return
	}

	if !req.URL.IsAbs() || req.URL.Scheme != schemeHTTP {
		http.Error(writer, "egress proxy: only absolute http URLs and CONNECT are carried", http.StatusBadRequest)

		return
	}

	p.forward.ServeHTTP(writer, req)
}

// tunnel opens a CONNECT tunnel to req.Host through the guard.
func (p *egressProxy) tunnel(writer http.ResponseWriter, req *http.Request) {
	if _, port, err := net.SplitHostPort(req.Host); err != nil || port == "" {
		http.Error(writer, "egress proxy: CONNECT needs host:port", http.StatusBadRequest)

		return
	}

	upstream, err := p.dial(req.Context(), "tcp", req.Host)
	if err != nil {
		p.refuse(writer, err)

		return
	}

	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		_ = upstream.Close()

		http.Error(writer, "egress proxy: tunneling unsupported", http.StatusInternalServerError)

		return
	}

	client, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()

		return
	}

	if !p.track(client, upstream) {
		return
	}

	defer p.untrack(client, upstream)

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	done := make(chan struct{}, 2)

	go func() {
		// Bytes the client sent after its CONNECT request line are already
		// in the hijacked buffer.
		_, _ = io.Copy(upstream, buffered)
		closeWrite(upstream)

		done <- struct{}{}
	}()

	go func() {
		_, _ = io.Copy(client, upstream)
		closeWrite(client)

		done <- struct{}{}
	}()

	<-done
	<-done
}

// track registers a tunnel's two ends so Close can cut them. It refuses (and
// closes them) once the proxy is closed.
func (p *egressProxy) track(conns ...net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		for _, conn := range conns {
			_ = conn.Close()
		}

		return false
	}

	for _, conn := range conns {
		p.tunnels[conn] = struct{}{}
	}

	return true
}

func (p *egressProxy) untrack(conns ...net.Conn) {
	p.mu.Lock()

	for _, conn := range conns {
		delete(p.tunnels, conn)
	}

	p.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
}

// closeWrite half-closes a TCP connection so the other side sees EOF, or
// closes it outright when it cannot be half-closed.
func closeWrite(conn net.Conn) {
	if tcp, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = tcp.CloseWrite()

		return
	}

	_ = conn.Close()
}
