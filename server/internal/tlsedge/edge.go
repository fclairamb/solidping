package tlsedge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
	"go.uber.org/zap"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
)

// Certificate states reported to the dashboard through
// statuspages.CertStatusProvider.
const (
	// CertStatusNone means nothing has been issued for the domain yet — the
	// normal state until the first HTTPS request arrives (issuance is
	// on-demand).
	CertStatusNone = "none"
	// CertStatusIssued means a certificate for the domain is in storage.
	CertStatusIssued = "issued"
	// CertStatusError means the last issuance attempt failed; the reason is in
	// the server log, keyed by domain.
	CertStatusError = "error"
)

const (
	// certStatusCacheTTL bounds how long a domain's certificate state is
	// cached, so the dashboard poll does not hit storage on every request while
	// still reflecting a fresh issuance quickly.
	certStatusCacheTTL = 30 * time.Second
	// servableCacheTTL bounds how long a POSITIVE "this host is ours" answer is
	// reused. It is the knob behind the documented "serving stops within the
	// cache TTL" behavior after a domain is unmapped, and it keeps a busy
	// status page from doing a database lookup on every TLS handshake.
	//
	// Negative answers are deliberately NOT cached: a domain that was just
	// verified in the dashboard must start serving on the very next request,
	// and an unknown host is refused before any expensive work anyway.
	servableCacheTTL = 30 * time.Second
	// readHeaderTimeout bounds slow-header attacks on the two extra listeners.
	readHeaderTimeout = 10 * time.Second
	// httpRedirectStatus is the permanent redirect used for plain HTTP.
	// 308 preserves the method and body, unlike 301.
	httpRedirectStatus = http.StatusPermanentRedirect
)

// ErrHostNotAllowed is returned by the decision func for any hostname that is
// neither one of the instance's own hosts nor a verified, servable custom
// domain. It is the ONLY thing standing between a hostile SNI scan and Let's
// Encrypt's failed-validation rate limit (5/hour/hostname), so it must stay
// fail-closed.
var ErrHostNotAllowed = errors.New("tlsedge: hostname not allowed for certificate issuance")

// Wiring errors returned by New.
var (
	// ErrNoDatabase is returned when the edge is built without a database
	// service to persist TLS assets in.
	ErrNoDatabase = errors.New("tlsedge: a database service is required")
	// ErrNoServableFunc is returned when the edge is built without the
	// custom-domain gate — the edge must never be able to default to "allow".
	ErrNoServableFunc = errors.New("tlsedge: a CustomDomainServable func is required")
)

// Options configures the edge.
type Options struct {
	// ACME is the acme.* config block.
	ACME config.ACMEConfig
	// DB backs the certmagic storage.
	DB db.Service
	// ReservedHosts are the instance's own hostnames (base-url host, docs host,
	// CNAME target) — self-hosters get TLS for the main host too, not just for
	// customer domains.
	ReservedHosts []string
	// CustomDomainServable reports whether a hostname currently resolves to a
	// verified, enabled, public status page. Required.
	CustomDomainServable func(ctx context.Context, host string) bool
	// Handler receives every request that arrives over the TLS listener. It is
	// the same top-level chain the plain listener uses, so custom-host routing
	// (status page + allowlisted API only) applies unchanged.
	Handler http.Handler
	// TrustedRoots optionally overrides the roots used to talk to the ACME CA.
	// Production leaves it nil (the system pool); it exists for a private or
	// test CA such as Pebble.
	TrustedRoots *x509.CertPool
	// Logger is used for issuance/serving diagnostics. Defaults to slog.Default.
	Logger *slog.Logger
}

// Edge owns the certmagic configuration and the two extra listeners that make
// in-server TLS work: :80 (ACME HTTP-01 challenge + redirect to https) and :443
// (TLS termination feeding the normal handler chain).
type Edge struct {
	opts   Options
	log    *slog.Logger
	magic  *certmagic.Config
	cache  *certmagic.Cache
	issuer *certmagic.ACMEIssuer
	store  *Storage

	httpSrv  *http.Server
	httpsSrv *http.Server
	// httpLn / httpsLn are the bound listeners, kept so Start can fail
	// synchronously on a bind error and so tests can inspect what the two
	// servers are actually serving on.
	httpLn  net.Listener
	httpsLn net.Listener

	mu sync.RWMutex
	// lastErr records the most recent issuance failure per domain so the
	// dashboard can explain why HTTPS is not live yet.
	lastErr map[string]time.Time
	// statusCache is a tiny TTL cache over the storage lookup behind
	// CertStatus.
	statusCache map[string]certStatusEntry
	// servableCache memoizes positive hostIsLocal answers only — see
	// servableCacheTTL.
	servableCache map[string]time.Time
}

type certStatusEntry struct {
	status    string
	expiresAt time.Time
}

// New builds the edge. It does not listen yet — call Start.
func New(opts *Options) (*Edge, error) {
	if opts == nil || opts.DB == nil {
		return nil, ErrNoDatabase
	}

	if opts.CustomDomainServable == nil {
		return nil, ErrNoServableFunc
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	edge := &Edge{
		opts:          *opts,
		log:           log,
		store:         NewStorage(opts.DB),
		lastErr:       make(map[string]time.Time),
		statusCache:   make(map[string]certStatusEntry),
		servableCache: make(map[string]time.Time),
	}

	edge.buildCertmagic()

	return edge, nil
}

// buildCertmagic wires the certmagic cache, config and ACME issuer. A dedicated
// cache (rather than certmagic's package-global default) keeps the storage and
// on-demand policy scoped to this server instance, which matters for tests and
// for a process that hosts more than one config.
func (e *Edge) buildCertmagic() {
	// certmagic logs through zap; a Nop logger would hide issuance failures, so
	// the interesting events are bridged onto slog via OnEvent below instead.
	zapLogger := zap.NewNop()

	template := certmagic.Config{
		Storage: e.store,
		Logger:  zapLogger,
		OnDemand: &certmagic.OnDemandConfig{
			DecisionFunc: e.decide,
		},
		OnEvent: e.onEvent,
	}

	e.cache = certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			return certmagic.New(e.cache, template), nil
		},
		Logger: zapLogger,
	})

	e.magic = certmagic.New(e.cache, template)

	issuerTemplate := certmagic.ACMEIssuer{
		CA:           strings.TrimSpace(e.opts.ACME.CAURL),
		Email:        strings.TrimSpace(e.opts.ACME.Email),
		Agreed:       true,
		Logger:       zapLogger,
		TrustedRoots: e.opts.TrustedRoots,
		// Point certmagic's built-in challenge solvers at the ports THIS edge
		// binds. They then always find the address in use and defer to our own
		// handlers (the :80 challenge handler and the :443 TLS config) instead
		// of trying to bind privileged ports we may not own — which is what
		// happens when the listen addresses are remapped (container port
		// forwarding, non-root deployment, tests).
		AltHTTPPort:    portFromListenAddr(e.opts.ACME.ListenHTTP),
		AltTLSALPNPort: portFromListenAddr(e.opts.ACME.ListenHTTPS),
	}

	e.issuer = certmagic.NewACMEIssuer(e.magic, issuerTemplate)
	e.magic.Issuers = []certmagic.Issuer{e.issuer}
}

// decide is certmagic's OnDemandConfig.DecisionFunc: the single gate on
// certificate issuance. A hostname is allowed iff it is one of the instance's
// own hosts or a verified, enabled, public custom domain. Everything else is
// refused before any CA traffic happens, which both blocks certificate
// squatting and protects the Let's Encrypt failed-validation rate limit.
func (e *Edge) decide(ctx context.Context, name string) error {
	host := normalizeHost(name)
	if host == "" {
		return fmt.Errorf("%w: empty hostname", ErrHostNotAllowed)
	}

	if e.hostIsLocal(ctx, host) {
		return nil
	}

	e.log.WarnContext(ctx, "tlsedge: refusing certificate for unknown host", "host", host)

	return fmt.Errorf("%w: %s", ErrHostNotAllowed, host)
}

// hostIsLocal is the single definition of "this instance serves that name":
// one of its own hostnames, or a verified, enabled, public custom domain. It
// gates certificate issuance (decide) and, when a fallback upstream is
// configured, whether a connection is terminated here or handed to the next
// hop — the two must never disagree, or a connection would be kept locally only
// to be refused a certificate.
func (e *Edge) hostIsLocal(ctx context.Context, host string) bool {
	host = normalizeHost(host)
	if host == "" {
		return false
	}

	for _, reserved := range e.opts.ReservedHosts {
		if host == normalizeHost(reserved) {
			return true
		}
	}

	if e.servableCached(host) {
		return true
	}

	servable := e.opts.CustomDomainServable(ctx, host)
	if servable {
		e.mu.Lock()
		e.servableCache[host] = time.Now().Add(servableCacheTTL)
		e.mu.Unlock()
	}

	return servable
}

// servableCached reports a still-valid positive answer for host.
func (e *Edge) servableCached(host string) bool {
	e.mu.RLock()
	expiresAt, ok := e.servableCache[host]
	e.mu.RUnlock()

	return ok && time.Now().Before(expiresAt)
}

// ForgetDomain drops every trace of a hostname from this edge: the positive
// servable cache, the certificate-status cache, the last-error note, and the
// certificate itself (in-memory cache and persistent storage).
//
// Call it the moment a domain stops being ours — unmapped by an operator, or
// demoted by the re-verification sweep. Without it the certificate keeps
// sitting in tls_storage until natural expiry, and even though hostIsLocal now
// refuses to serve it, leaving a live key for a domain someone else may control
// is not something to keep around.
func (e *Edge) ForgetDomain(ctx context.Context, domain string) {
	host := normalizeHost(domain)
	if host == "" {
		return
	}

	e.mu.Lock()
	delete(e.servableCache, host)
	delete(e.statusCache, host)
	delete(e.lastErr, host)
	e.mu.Unlock()

	// Drop it from the in-memory cache first, so a handshake racing this call
	// cannot still be handed the cached certificate.
	cached := e.cache.AllMatchingCertificates(host)
	hashes := make([]string, 0, len(cached))

	for i := range cached {
		hashes = append(hashes, cached[i].Hash())
	}

	e.cache.Remove(hashes)

	if err := e.store.DeleteSite(ctx, e.issuer.IssuerKey(), host); err != nil {
		e.log.WarnContext(ctx, "tlsedge: could not delete stored certificate for an unmapped domain",
			"domain", host, "error", err)

		return
	}

	e.log.InfoContext(ctx, "tlsedge: dropped the certificate for a domain we no longer serve", "domain", host)
}

// Decide exposes the decision func for tests and for callers that want to check
// a hostname without triggering a handshake.
func (e *Edge) Decide(ctx context.Context, name string) error {
	return e.decide(ctx, name)
}

// onEvent bridges certmagic's issuance lifecycle onto slog and records the last
// failure per domain so CertStatus can report it.
func (e *Edge) onEvent(ctx context.Context, event string, data map[string]any) error {
	identifier, _ := data["identifier"].(string)

	switch event {
	case "cert_obtaining":
		e.log.InfoContext(ctx, "tlsedge: obtaining certificate", "domain", identifier)
	case "cert_obtained":
		e.log.InfoContext(ctx, "tlsedge: certificate obtained", "domain", identifier)
		e.clearError(identifier)
		e.invalidateStatus(identifier)
	case "cert_failed":
		err, _ := data["error"].(error)
		e.log.ErrorContext(ctx, "tlsedge: certificate issuance failed", "domain", identifier, "error", err)
		e.recordError(identifier)
		e.invalidateStatus(identifier)
	}

	return nil
}

func (e *Edge) recordError(domain string) {
	host := normalizeHost(domain)
	if host == "" {
		return
	}

	e.mu.Lock()
	e.lastErr[host] = time.Now()
	e.mu.Unlock()
}

func (e *Edge) clearError(domain string) {
	host := normalizeHost(domain)

	e.mu.Lock()
	delete(e.lastErr, host)
	e.mu.Unlock()
}

func (e *Edge) invalidateStatus(domain string) {
	host := normalizeHost(domain)

	e.mu.Lock()
	delete(e.statusCache, host)
	e.mu.Unlock()
}

// TLSConfig returns the tls.Config that terminates connections with on-demand
// certificates. It keeps the ACME-TLS-ALPN protocol intact so TLS-ALPN-01
// challenges can be solved on the same listener.
func (e *Edge) TLSConfig(ctx context.Context) *tls.Config {
	cfg := e.magic.TLSConfig()
	cfg.MinVersion = tls.VersionTLS12
	cfg.NextProtos = append(cfg.NextProtos, "h2", "http/1.1")

	// Gate SERVING on the same predicate that gates issuance.
	//
	// decide() only runs when certmagic needs to OBTAIN a certificate. Once one
	// is in the cache or in tls_storage, certmagic hands it out without asking
	// anyone — so a domain that was unmapped, or demoted by the re-verification
	// sweep, kept completing handshakes with a valid publicly-trusted
	// certificate for up to 90 days. Worse, with no custom-domain mapping left,
	// the request fell through to the instance's own-host routing and served
	// the dashboard SPA on a hostname this installation no longer claims. The
	// custom-host path allowlist that 404s /dash0 only applies while the
	// mapping exists.
	//
	// Refusing here closes the connection instead, which is the behavior
	// "serving stops" in the takeover-protection docs always described. It also
	// covers the window before any storage cleanup runs, which is why it is the
	// half that actually matters.
	issue := cfg.GetCertificate
	cfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		// The edge's lifetime context is used rather than hello.Context().
		// The per-handshake context would give us cancellation when a client
		// disconnects mid-handshake, but it is not derived from ours, and a
		// synthetic hello (tests, or any caller building one by hand) reports
		// nil — which would panic the moment it reached a database call. The
		// lookup behind this is a single indexed query, memoized by
		// servableCache, so losing that cancellation costs nothing.
		host := normalizeHost(hello.ServerName)
		if host == "" {
			return nil, fmt.Errorf("%w: no SNI", ErrHostNotAllowed)
		}

		if !e.hostIsLocal(ctx, host) {
			e.log.WarnContext(ctx, "tlsedge: refusing to serve a host we do not own", "host", host)

			return nil, fmt.Errorf("%w: %s", ErrHostNotAllowed, host)
		}

		return issue(hello)
	}

	return cfg
}

// Start brings up the :80 and :443 listeners. It returns once both are bound
// (or immediately on a bind failure) and serves in background goroutines.
func (e *Edge) Start(ctx context.Context) error {
	httpLn, err := e.listen(ctx, e.opts.ACME.ListenHTTP, listenerHTTP)
	if err != nil {
		return err
	}

	httpsLn, err := e.listen(ctx, e.opts.ACME.ListenHTTPS, listenerHTTPS)
	if err != nil {
		_ = httpLn.Close()

		return err
	}

	e.httpLn, e.httpsLn = httpLn, httpsLn

	e.httpSrv = &http.Server{
		Addr:              e.opts.ACME.ListenHTTP,
		Handler:           e.challengeHandler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	e.httpsSrv = &http.Server{
		Addr:              e.opts.ACME.ListenHTTPS,
		Handler:           e.opts.Handler,
		TLSConfig:         e.TLSConfig(ctx),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	e.log.InfoContext(ctx, "tlsedge: starting in-server TLS",
		"http", e.opts.ACME.ListenHTTP, "https", e.opts.ACME.ListenHTTPS, "ca", e.caLabel(),
		"proxy_protocol", e.opts.ACME.ProxyProtocol,
		"fallback_upstream_http", e.opts.ACME.FallbackUpstreamHTTP,
		"fallback_upstream_https", e.opts.ACME.FallbackUpstreamHTTPS,
		"fallback_upstream_proxy_protocol", e.opts.ACME.FallbackUpstreamProxyProtocol)

	// The listeners outlive the caller's context (they are stopped by
	// Shutdown), so log against a cancellation-free copy of it.
	logCtx := context.WithoutCancel(ctx)

	go func() {
		if err := e.httpSrv.Serve(httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			e.log.ErrorContext(logCtx, "tlsedge: ACME HTTP listener stopped", "error", err)
		}
	}()

	go func() {
		// Certificates come from the TLSConfig (on-demand), so no cert/key file.
		if err := e.httpsSrv.ServeTLS(httpsLn, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			e.log.ErrorContext(logCtx, "tlsedge: HTTPS listener stopped", "error", err)
		}
	}()

	return nil
}

// listen binds one of the edge's addresses and, when acme.proxy_protocol is on,
// wraps it so the PROXY preamble is consumed below TLS. BOTH listeners are
// wrapped: the plain one carries the ACME HTTP-01 challenge and the redirect to
// https, and its client IP feeds the same rate limiter as the TLS one.
//
// When a fallback upstream is configured for this listener, a splitter goes on
// top — deliberately ABOVE the PROXY wrapper, so the connection it inspects
// already knows the real client address and the forwarded PROXY header can
// carry it on to the next hop. Without an upstream nothing is added at all and
// the listener behaves exactly as before.
func (e *Edge) listen(ctx context.Context, addr, kind string) (net.Listener, error) {
	var lc net.ListenConfig

	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tlsedge: cannot listen on %s: %w", addr, err)
	}

	wrapped, err := wrapProxyProtocol(listener, &e.opts.ACME)
	if err != nil {
		_ = listener.Close()

		return nil, err
	}

	split, err := e.wrapFallback(ctx, wrapped, kind)
	if err != nil {
		_ = wrapped.Close()

		return nil, err
	}

	return split, nil
}

// wrapFallback installs the unknown-host splitter on a listener when the
// matching acme.fallback_upstream_* address is set.
func (e *Edge) wrapFallback(ctx context.Context, listener net.Listener, kind string) (net.Listener, error) {
	upstream := strings.TrimSpace(e.opts.ACME.FallbackUpstreamHTTPS)
	peek := peekTLSClientHello

	if kind == listenerHTTP {
		upstream = strings.TrimSpace(e.opts.ACME.FallbackUpstreamHTTP)
		peek = peekHTTPHost
	}

	if upstream == "" {
		return listener, nil
	}

	split, err := newFallbackListener(ctx, listener, fallbackConfig{
		listener:      kind,
		upstream:      upstream,
		proxyProtocol: e.opts.ACME.FallbackUpstreamProxyProtocol,
		peek:          peek,
		peekTimeout:   readHeaderTimeout,
		isLocal:       e.hostIsLocal,
		log:           e.log,
	})
	if err != nil {
		return nil, err
	}

	return split, nil
}

// caLabel is a log-friendly name for the configured CA.
func (e *Edge) caLabel() string {
	if ca := strings.TrimSpace(e.opts.ACME.CAURL); ca != "" {
		return ca
	}

	return "letsencrypt-production"
}

// challengeHandler serves the ACME HTTP-01 challenge and 308-redirects
// everything else to https on the same host. The redirect deliberately does not
// look at the routing tables: any host that got here already resolved to us.
func (e *Edge) challengeHandler() http.Handler {
	redirect := http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		target := "https://" + hostOnly(req.Host) + req.URL.RequestURI()
		http.Redirect(writer, req, target, httpRedirectStatus)
	})

	return e.issuer.HTTPChallengeHandler(redirect)
}

// Shutdown gracefully stops both listeners and the certificate-maintenance
// goroutine.
func (e *Edge) Shutdown(ctx context.Context) error {
	var errs []error

	if e.httpSrv != nil {
		if err := e.httpSrv.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if e.httpsSrv != nil {
		if err := e.httpsSrv.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if e.cache != nil {
		e.cache.Stop()
	}

	return errors.Join(errs...)
}

// CertStatus reports the certificate state of a custom domain for the
// dashboard: "issued" once certificate material exists in storage, "error"
// when the last issuance attempt failed, "none" otherwise.
func (e *Edge) CertStatus(domain string) string {
	host := normalizeHost(domain)
	if host == "" {
		return CertStatusNone
	}

	e.mu.RLock()
	entry, cached := e.statusCache[host]
	e.mu.RUnlock()

	if cached && time.Now().Before(entry.expiresAt) {
		return entry.status
	}

	status := e.computeCertStatus(host)

	e.mu.Lock()
	e.statusCache[host] = certStatusEntry{status: status, expiresAt: time.Now().Add(certStatusCacheTTL)}
	e.mu.Unlock()

	return status
}

// computeCertStatus does the uncached lookup: storage first (an issued cert
// beats a stale error), then the failure map.
func (e *Edge) computeCertStatus(host string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if e.hasCertificate(ctx, host) {
		return CertStatusIssued
	}

	e.mu.RLock()
	_, failed := e.lastErr[host]
	e.mu.RUnlock()

	if failed {
		return CertStatusError
	}

	return CertStatusNone
}

// hasCertificate reports whether storage holds certificate material for host.
// The issuer key is part of the storage path and depends on the configured CA,
// so the scan matches on the "<host>/<host>.crt" suffix rather than
// reconstructing the full key.
func (e *Edge) hasCertificate(ctx context.Context, host string) bool {
	keys, err := e.store.List(ctx, "certificates", true)
	if err != nil {
		return false
	}

	suffix := "/" + host + "/" + host + ".crt"
	for _, key := range keys {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}

	return false
}

// portFromListenAddr extracts the numeric port from a ":port" / "host:port"
// listen address. It returns 0 (certmagic's default) when the address has no
// parseable port.
func portFromListenAddr(addr string) int {
	_, portStr, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return 0
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}

	return port
}

// normalizeHost lowercases a hostname and strips a trailing dot and any port.
func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostOnly(host)), "."))
}

// hostOnly strips a :port suffix from a Host header value, leaving IPv6
// literals intact.
func hostOnly(host string) string {
	if idx := strings.LastIndex(host, ":"); idx > strings.LastIndex(host, "]") {
		return host[:idx]
	}

	return host
}
