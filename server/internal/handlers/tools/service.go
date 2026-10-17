// Package tools runs one SolidPing check once, anonymously, for the public
// tools of www.solidping.io (spec 2026-10-14-02): website down checker, SSL
// certificate checker, DNS lookup and port checker.
//
// Nothing is persisted. Every run goes through an egress guard that is
// hardwired to refuse non-public targets, whatever SP_EGRESS_ALLOW_PRIVATE
// says: that switch opens a LAN to the check workers of the instance, never to
// anonymous visitors.
package tools

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/fclairamb/solidping/server/internal/checkers/checkdns"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssl"
	"github.com/fclairamb/solidping/server/internal/checkers/checktcp"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// Tool types offered in v1.
const (
	TypeHTTP = "http"
	TypeSSL  = "ssl"
	TypeDNS  = "dns"
	TypeTCP  = "tcp"
)

const (
	// RunTimeout bounds one run, resolution included.
	RunTimeout = 10 * time.Second
	// MaxRedirects is how many redirects an http run follows.
	MaxRedirects = 5
	// CacheTTL is how long the result for one (type, target, options) is
	// served from memory, so a popular target costs one request a minute.
	CacheTTL = 60 * time.Second
	// MaxConcurrentRuns is the global cap on runs in flight.
	MaxConcurrentRuns = 16

	maxCacheEntries = 10000
	maxTargetLength = 2048
	maxHostLength   = 253
	maxLabelLength  = 63
	defaultSSLPort  = 443
	maxPort         = 65535
	hoursPerDay     = 24
)

// Errors a run ends with. Each maps to one error code of the endpoint.
var (
	ErrInvalidTarget    = errors.New("invalid target")
	ErrTargetNotAllowed = errors.New("target not allowed")
	ErrResolveFailed    = errors.New("target could not be resolved")
	ErrTimeout          = errors.New("run timed out")
	ErrBusy             = errors.New("too many runs in progress")
)

// dnsRecordTypes are the record types the DNS tool queries.
//
//nolint:gochecknoglobals // constant lookup table
var dnsRecordTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true, "NS": true, "CAA": true,
}

// Request is the body of POST /api/v1/tools/run.
type Request struct {
	Type    string  `json:"type"`
	Target  string  `json:"target"`
	Options Options `json:"options"`
}

// Options are the few safe knobs a visitor may set.
type Options struct {
	// Record is the DNS record type (dns only), A by default.
	Record string `json:"record,omitempty"`
}

// Result is the normalised answer of one run, the same shape for every type.
type Result struct {
	Type       string         `json:"type"`
	Target     string         `json:"target"`
	Status     string         `json:"status"`
	DurationMs int64          `json:"durationMs"`
	CheckedAt  time.Time      `json:"checkedAt"`
	Region     string         `json:"region,omitempty"`
	Cached     bool           `json:"cached"`
	Details    map[string]any `json:"details"`
	Monitor    Monitor        `json:"monitor"`
}

// Monitor turns a one-off result into a prefilled "monitor this" form.
type Monitor struct {
	CheckType string `json:"checkType"`
	// Query is the query string the dashboard's checks/new route reads.
	Query string `json:"query"`
}

// Params configures a Service.
type Params struct {
	// DeniedHosts are SolidPing's own public hosts (and their subdomains),
	// which the endpoint refuses to probe.
	DeniedHosts []string
	// Region is reported on every result.
	Region string
}

// Option tunes a Service; tests use it to fake DNS.
type Option func(*Service)

// WithLookup replaces the resolver used to vet targets and by the guard.
func WithLookup(lookup egress.LookupFunc) Option {
	return func(s *Service) { s.lookup = lookup }
}

// WithGuardOptions adds options to the egress guard (tests only).
func WithGuardOptions(opts ...egress.Option) Option {
	return func(s *Service) { s.guardOpts = append(s.guardOpts, opts...) }
}

// WithTimeout replaces RunTimeout (tests only).
func WithTimeout(timeout time.Duration) Option {
	return func(s *Service) { s.timeout = timeout }
}

type outcome struct {
	result  *Result
	err     error
	expires time.Time
}

// Service runs tools. It holds no database handle: nothing it does is stored.
type Service struct {
	deniedHosts []string
	region      string
	timeout     time.Duration
	lookup      egress.LookupFunc
	guardOpts   []egress.Option
	guard       *egress.Guard
	slots       chan struct{}
	flight      singleflight.Group
	now         func() time.Time

	mu    sync.Mutex
	cache map[string]outcome
}

// NewService builds a Service whose guard always refuses non-public targets.
func NewService(params Params, opts ...Option) *Service {
	svc := &Service{
		deniedHosts: params.DeniedHosts,
		region:      params.Region,
		timeout:     RunTimeout,
		lookup:      checkerdef.LookupIPAddr,
		slots:       make(chan struct{}, MaxConcurrentRuns),
		now:         time.Now,
		cache:       make(map[string]outcome),
	}

	for _, opt := range opts {
		opt(svc)
	}

	// Hardwired: false, never the instance's egress policy.
	svc.guard = egress.New(false, append([]egress.Option{egress.WithLookup(svc.lookup)}, svc.guardOpts...)...)

	return svc
}

// target is a request reduced to what a run needs.
type target struct {
	typ    string
	host   string
	port   int
	url    *url.URL // http only
	record string   // dns only
	label  string   // normalised target, echoed in the result
}

func (t *target) key() string {
	return t.typ + "\x00" + t.label + "\x00" + t.record
}

// Run executes one tool run, or answers it from the cache.
func (s *Service) Run(ctx context.Context, req *Request) (*Result, bool, error) {
	tgt, err := parseTarget(req)
	if err != nil {
		return nil, false, err
	}

	if tgt.typ != TypeDNS && s.isDeniedHost(tgt.host) {
		return nil, false, ErrTargetNotAllowed
	}

	key := tgt.key()
	if out, ok := s.cached(key); ok {
		return out.result, true, out.err
	}

	value, err, _ := s.flight.Do(key, func() (any, error) {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			return nil, ErrBusy
		}

		// Detached from the caller: a shared run must not die with the first
		// visitor who closed the tab.
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
		defer cancel()

		result, runErr := s.run(runCtx, tgt)
		if result != nil || errors.Is(runErr, ErrTimeout) {
			s.store(key, outcome{result: result, err: runErr})
		}

		return result, runErr
	})

	result, _ := value.(*Result)

	return result, false, err
}

func (s *Service) cached(key string) (outcome, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out, ok := s.cache[key]
	if !ok || !s.now().Before(out.expires) {
		return outcome{}, false
	}

	return out, true
}

func (s *Service) store(key string, out outcome) {
	now := s.now()
	out.expires = now.Add(CacheTTL)

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.cache) >= maxCacheEntries {
		for k := range s.cache {
			if !now.Before(s.cache[k].expires) {
				delete(s.cache, k)
			}
		}

		if len(s.cache) >= maxCacheEntries {
			return
		}
	}

	s.cache[key] = out
}

func (s *Service) isDeniedHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")

	for _, denied := range s.deniedHosts {
		if host == denied || strings.HasSuffix(host, "."+denied) {
			return true
		}
	}

	return false
}

func (s *Service) run(ctx context.Context, tgt *target) (*Result, error) {
	ctx = egress.WithGuard(ctx, s.guard)
	ctx, denials := egress.WithRecorder(ctx)

	var (
		ip  net.IP
		err error
	)

	if tgt.typ != TypeDNS {
		ip, err = s.vet(ctx, tgt.host)
		if err != nil {
			return nil, err
		}
	}

	start := s.now()

	var (
		res     *checkerdef.Result
		details map[string]any
	)

	switch tgt.typ {
	case TypeHTTP:
		res, details, err = s.runHTTP(ctx, tgt, ip)
	case TypeSSL:
		res, err = (&checkssl.SSLChecker{}).Execute(ctx, &checkssl.SSLConfig{
			Host: ip.String(), Port: tgt.port, ServerName: tgt.host, Timeout: s.timeout,
		})
		details = sslDetails(res)
	case TypeTCP:
		res, err = (&checktcp.TCPChecker{}).Execute(ctx, &checktcp.TCPConfig{
			Host: ip.String(), Port: tgt.port, Timeout: s.timeout,
		})
		details = tcpDetails(res)
	case TypeDNS:
		res, err = (&checkdns.DNSChecker{}).Execute(ctx, &checkdns.DNSConfig{
			Host: tgt.host, RecordType: tgt.record, Timeout: s.timeout,
		})
		if err != nil {
			return nil, ErrResolveFailed
		}

		details = dnsDetails(res, tgt.record)
	}

	if denials.Denied() != nil || errors.Is(err, ErrTargetNotAllowed) {
		return nil, ErrTargetNotAllowed
	}

	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrTimeout
		}

		return nil, fmt.Errorf("run %s check: %w", tgt.typ, err)
	}

	if res.Status == checkerdef.StatusTimeout {
		return nil, ErrTimeout
	}

	return &Result{
		Type:       tgt.typ,
		Target:     tgt.label,
		Status:     res.Status.String(),
		DurationMs: durationMs(res.Duration, s.now().Sub(start)),
		CheckedAt:  start.UTC().Truncate(time.Second),
		Region:     s.region,
		Details:    details,
		Monitor:    monitorFor(tgt),
	}, nil
}

// vet resolves host and refuses it when ANY address it resolves to is
// non-public: a name with one public and one private answer is refused, not
// filtered. It returns the address to connect to.
func (s *Service) vet(ctx context.Context, host string) (net.IP, error) {
	ip, err := egress.ParseURLHost(host)
	if err != nil {
		return nil, ErrInvalidTarget
	}

	if ip != nil {
		if s.guard.CheckAddr(ctx, host, ip) != nil {
			return nil, ErrTargetNotAllowed
		}

		return ip, nil
	}

	addrs, err := s.lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		if ctx.Err() != nil {
			return nil, ErrTimeout
		}

		return nil, ErrResolveFailed
	}

	for i := range addrs {
		if s.guard.CheckAddr(ctx, host, addrs[i].IP) != nil {
			return nil, ErrTargetNotAllowed
		}
	}

	return addrs[0].IP, nil
}

func (s *Service) runHTTP(ctx context.Context, tgt *target, ip net.IP) (*checkerdef.Result, map[string]any, error) {
	probe := *tgt.url

	// A numeric host a browser reads as an address (2130706433, 127.1) is
	// dialed as that address, never handed to DNS as a name.
	if parsed, _ := egress.ParseURLHost(tgt.host); parsed != nil {
		probe.Host = hostPort(ip.String(), probe.Port())
	}

	var (
		lock      sync.Mutex
		leafState *tls.ConnectionState
		ownHost   bool
	)

	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			if err != nil || len(state.PeerCertificates) == 0 {
				return
			}

			lock.Lock()
			defer lock.Unlock()

			leafState = &state
		},
	})

	checker := &checkhttp.HTTPChecker{
		MaxRedirects: MaxRedirects,
		RedirectGuard: func(req *http.Request) error {
			if s.isDeniedHost(req.URL.Hostname()) {
				lock.Lock()
				ownHost = true
				lock.Unlock()

				return ErrTargetNotAllowed
			}

			return nil
		},
	}

	res, err := checker.Execute(ctx, &checkhttp.HTTPConfig{URL: probe.String(), Method: http.MethodGet})

	lock.Lock()
	defer lock.Unlock()

	if ownHost {
		return nil, nil, ErrTargetNotAllowed
	}

	if err != nil {
		return nil, nil, fmt.Errorf("http check: %w", err)
	}

	return res, httpDetails(res, probe.String(), leafState, time.Now()), nil
}

func parseTarget(req *Request) (*target, error) {
	raw := strings.TrimSpace(req.Target)
	if raw == "" || len(raw) > maxTargetLength {
		return nil, ErrInvalidTarget
	}

	switch strings.ToLower(strings.TrimSpace(req.Type)) {
	case TypeHTTP:
		return parseHTTPTarget(raw)
	case TypeSSL:
		return parseHostPortTarget(TypeSSL, raw, defaultSSLPort)
	case TypeTCP:
		return parseHostPortTarget(TypeTCP, raw, 0)
	case TypeDNS:
		return parseDNSTarget(raw, req.Options.Record)
	default:
		return nil, ErrInvalidTarget
	}
}

func parseHTTPTarget(raw string) (*target, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, ErrInvalidTarget
	}

	host := strings.ToLower(parsed.Hostname())
	if !validHost(host) {
		return nil, ErrInvalidTarget
	}

	port := 0
	if p := parsed.Port(); p != "" {
		if port = parsePort(p); port == 0 {
			return nil, ErrInvalidTarget
		}
	}

	parsed.Host = hostPort(host, parsed.Port())
	parsed.Fragment = ""

	return &target{typ: TypeHTTP, host: host, port: port, url: parsed, label: parsed.String()}, nil
}

// parseHostPortTarget reads "host:port", "[v6]:port", a bare host (when
// defaultPort is set) or an http(s) URL (its host and port).
func parseHostPortTarget(typ, raw string, defaultPort int) (*target, error) {
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" {
			return nil, ErrInvalidTarget
		}

		raw = parsed.Host
	}

	host, portText, err := net.SplitHostPort(raw)
	if err != nil {
		// No port: a bare host, or a bare IPv6 literal.
		host = strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
		portText = ""
	}

	port := defaultPort
	if portText != "" {
		port = parsePort(portText)
	}

	host = strings.ToLower(host)
	if port == 0 || !validHost(host) {
		return nil, ErrInvalidTarget
	}

	return &target{typ: typ, host: host, port: port, label: net.JoinHostPort(host, strconv.Itoa(port))}, nil
}

func parseDNSTarget(raw, record string) (*target, error) {
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return nil, ErrInvalidTarget
		}

		raw = parsed.Hostname()
	}

	host := strings.TrimSuffix(strings.ToLower(raw), ".")
	if ip, err := egress.ParseURLHost(host); err != nil || ip != nil || !validHost(host) {
		return nil, ErrInvalidTarget
	}

	record = strings.ToUpper(strings.TrimSpace(record))
	if record == "" {
		record = "A"
	}

	if !dnsRecordTypes[record] {
		return nil, ErrInvalidTarget
	}

	return &target{typ: TypeDNS, host: host, record: record, label: host}, nil
}

// parsePort returns the port, or 0 when it is not in 1..65535.
func parsePort(text string) int {
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > maxPort {
		return 0
	}

	return port
}

func hostPort(host, port string) string {
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}

	if port == "" {
		return host
	}

	return host + ":" + port
}

// validHost accepts an IP literal or a DNS name made of letters, digits,
// hyphens and underscores.
func validHost(host string) bool {
	if host == "" || len(host) > maxHostLength {
		return false
	}

	if net.ParseIP(host) != nil {
		return true
	}

	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > maxLabelLength {
			return false
		}

		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
				return false
			}
		}
	}

	return true
}

func durationMs(measured, fallback time.Duration) int64 {
	if measured > 0 {
		return measured.Milliseconds()
	}

	return fallback.Milliseconds()
}

func outputError(res *checkerdef.Result, details map[string]any) {
	if msg, ok := res.Output[checkerdef.OutputKeyError].(string); ok && msg != "" {
		details["error"] = msg
	}
}

func httpDetails(res *checkerdef.Result, probeURL string, tlsState *tls.ConnectionState, now time.Time) map[string]any {
	details := map[string]any{"responseTimeMs": res.Duration.Milliseconds()}

	if code, ok := res.Output[checkerdef.OutputKeyStatusCode].(int); ok {
		details["statusCode"] = code
	}

	redirects, _ := res.Output["redirect_chain"].([]string)
	if redirects == nil {
		redirects = []string{}
	}

	details["redirects"] = redirects

	finalURL := probeURL
	if len(redirects) > 0 {
		finalURL = redirects[len(redirects)-1]
	}

	details["finalUrl"] = finalURL

	if proto, ok := res.Output["http_protocol"].(string); ok {
		details["httpProtocol"] = proto
	}

	if tlsState != nil && strings.HasPrefix(finalURL, "https://") {
		leaf := tlsState.PeerCertificates[0]
		details["certNotAfter"] = leaf.NotAfter.UTC().Format(time.RFC3339)
		details["certDaysLeft"] = int(leaf.NotAfter.Sub(now).Hours() / hoursPerDay)
	}

	outputError(res, details)

	return details
}

func sslDetails(res *checkerdef.Result) map[string]any {
	details := map[string]any{}
	if res == nil {
		return details
	}

	copyKeys := map[string]string{
		"issuer":         "issuer",
		"subject":        "subject",
		"not_before":     "notBefore",
		"not_after":      "notAfter",
		"days_remaining": "daysLeft",
		"dns_names":      "sans",
		"chainVerified":  "chainValid",
		"tls_version":    "tlsVersion",
	}

	for from, to := range copyKeys {
		if value, ok := res.Output[from]; ok {
			details[to] = value
		}
	}

	outputError(res, details)

	return details
}

func tcpDetails(res *checkerdef.Result) map[string]any {
	details := map[string]any{}
	if res == nil {
		return details
	}

	details["connected"] = res.Status == checkerdef.StatusUp

	if connect, ok := res.Metrics["connection_time_ms"]; ok && res.Status == checkerdef.StatusUp {
		details["connectTimeMs"] = connect
	}

	outputError(res, details)

	return details
}

func dnsDetails(res *checkerdef.Result, record string) map[string]any {
	records := []string{}

	for _, key := range []string{"resolved_ips", "resolved_values"} {
		if values, ok := res.Output[key].([]string); ok {
			records = append(records, values...)
		}
	}

	return map[string]any{"record": record, "records": records}
}

func monitorFor(tgt *target) Monitor {
	query := url.Values{"checkType": {tgt.typ}, "checkName": {tgt.host}}

	switch tgt.typ {
	case TypeHTTP:
		query.Set("httpUrl", tgt.label)
	case TypeSSL, TypeTCP:
		query.Set("host", tgt.host)
		query.Set("port", strconv.Itoa(tgt.port))
	case TypeDNS:
		query.Set("host", tgt.host)
	}

	return Monitor{CheckType: tgt.typ, Query: query.Encode()}
}
