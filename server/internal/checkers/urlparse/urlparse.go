// Package urlparse provides URL parsing for check configurations.
package urlparse

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Error definitions for URL parsing.
var (
	errEmptyURL          = errors.New("empty URL")
	errMissingHost       = errors.New("missing host in URL")
	errTCPRequiresPort   = errors.New("TCP URL requires a port (e.g., tcp://host:port)")
	errDNSRequiresDomain = errors.New("DNS URL requires a domain in path (e.g., dns://8.8.8.8/example.com)")
	errUnsupportedScheme = errors.New("unsupported scheme")
	errInvalidPort       = errors.New("invalid port")
	errUnixSocketHost    = errors.New("a unix:// URL has no host or port (e.g., unix:///run/app/app.sock)")
	errHTTPSUnix         = errors.New("https+unix is not supported: there is no TLS over a local socket")
	errHTTPUnixForm      = errors.New("an http+unix URL looks like http+unix:///run/app/app.sock:/health")
	errUnixSocketPath    = errors.New("a unix:// URL requires the socket path (e.g., unix:///run/app/app.sock)")
	errInvalidDNSType    = errors.New("invalid DNS record type")
)

// ParsedURL contains the parsed components of a check URL.
type ParsedURL struct {
	CheckType   checkerdef.CheckType
	Scheme      string // Original scheme (e.g., "https", "tcps", "dns")
	Host        string // Hostname or IP (for DNS: the resolver)
	Port        int    // Port number (0 if not specified)
	Path        string // URL path (for HTTP; for DNS: the domain to query)
	Query       string // Raw query string (for HTTP)
	TLS         bool   // Whether TLS/SSL is enabled
	RecordType  string // For DNS: A, AAAA, MX, etc.
	OriginalURL string // The original URL string

	// DNS-specific parsed fields
	DNSDomain string // For DNS: the domain to resolve (from path)

	// Socket is the Unix socket path of a `unix:///path` URL (spec
	// 2026-11-24-01); Host and Port are then empty.
	Socket string
}

//nolint:gochecknoglobals // schemeMapping is a constant lookup map.
var schemeMapping = map[string]checkerdef.CheckType{
	"http":   checkerdef.CheckTypeHTTP,
	"https":  checkerdef.CheckTypeHTTP,
	"tcp":    checkerdef.CheckTypeTCP,
	"tcps":   checkerdef.CheckTypeTCP,
	"ping":   checkerdef.CheckTypeICMP,
	"icmp":   checkerdef.CheckTypeICMP,
	"dns":    checkerdef.CheckTypeDNS,
	"domain": checkerdef.CheckTypeDomain,
	"whois":  checkerdef.CheckTypeDomain,
	"ws":     checkerdef.CheckTypeWebSocket,
	"wss":    checkerdef.CheckTypeWebSocket,
	"unix":   checkerdef.CheckTypeTCP,
	// http+unix:///run/app/app.sock:/health is an http check through a socket.
	"http+unix": checkerdef.CheckTypeHTTP,
	// https+unix is recognized only to be refused: no TLS over a local socket.
	"https+unix": checkerdef.CheckTypeHTTP,
	// redis+unix:///run/redis/redis.sock?db=0 and
	// postgres+unix:///var/run/postgresql/.s.PGSQL.5432?dbname=app&user=monitor
	// are redis / postgresql checks through a socket (spec 2026-11-24-03).
	schemeRedisUnix:    checkerdef.CheckTypeRedis,
	schemePostgresUnix: checkerdef.CheckTypePostgreSQL,
}

//nolint:gochecknoglobals // validDNSRecordTypes is a constant lookup map.
var validDNSRecordTypes = map[string]bool{
	"A": true, "AAAA": true, "MX": true, "TXT": true,
	"CNAME": true, "NS": true, "SOA": true, "PTR": true,
}

// Parse analyzes a URL and extracts check configuration.
func Parse(rawURL string) (*ParsedURL, error) {
	if rawURL == "" {
		return nil, errEmptyURL
	}

	parsed := &ParsedURL{OriginalURL: rawURL}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	parsed.Scheme = strings.ToLower(parsedURL.Scheme)

	// Determine check type
	checkType, ok := schemeMapping[parsed.Scheme]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errUnsupportedScheme, parsed.Scheme)
	}

	parsed.CheckType = checkType

	// Extract host and port
	parsed.Host = parsedURL.Hostname()

	if portStr := parsedURL.Port(); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("%w: %s", errInvalidPort, portStr)
		}

		parsed.Port = port
	}

	// Set TLS based on scheme
	parsed.TLS = parsed.Scheme == "https" || parsed.Scheme == "tcps"

	// Handle scheme-specific parsing
	if err := parsed.parseByType(parsedURL); err != nil {
		return nil, err
	}

	return parsed, nil
}

// parseByType handles scheme-specific URL parsing.
func (p *ParsedURL) parseByType(parsedURL *url.URL) error {
	//nolint:exhaustive // SSL is not yet implemented.
	switch p.CheckType {
	case checkerdef.CheckTypeHTTP:
		if p.Scheme == schemeHTTPUnix || p.Scheme == "https+unix" {
			return p.parseHTTPUnix(parsedURL)
		}

		if p.Host == "" {
			return errMissingHost
		}

		p.Path = parsedURL.Path
		p.Query = parsedURL.RawQuery

	case checkerdef.CheckTypeTCP:
		return p.parseTCP(parsedURL)

	case checkerdef.CheckTypeRedis, checkerdef.CheckTypePostgreSQL:
		return p.parseUnix(parsedURL)

	case checkerdef.CheckTypeICMP:
		if p.Host == "" {
			return errMissingHost
		}

	case checkerdef.CheckTypeDNS:
		if err := p.parseDNS(parsedURL); err != nil {
			return err
		}

	case checkerdef.CheckTypeDomain:
		if p.Host == "" {
			return errMissingHost
		}
	}

	return nil
}

// parseTCP handles tcp://, tcps:// and unix:// URLs.
func (p *ParsedURL) parseTCP(parsedURL *url.URL) error {
	if p.Scheme == "unix" {
		return p.parseUnix(parsedURL)
	}

	if p.Host == "" {
		return errMissingHost
	}

	if p.Port == 0 {
		return errTCPRequiresPort
	}

	return nil
}

// parseUnix handles `unix:///run/app/app.sock` (and `unix:/run/app/app.sock`):
// the path is the socket, and there is no host or port.
func (p *ParsedURL) parseUnix(parsedURL *url.URL) error {
	if p.Host != "" || p.Port != 0 {
		return errUnixSocketHost
	}

	if parsedURL.Path == "" {
		return errUnixSocketPath
	}

	p.Socket = parsedURL.Path
	p.Query = parsedURL.RawQuery

	return nil
}

// Schemes of the redis and postgres checks through a Unix socket.
const (
	schemeRedisUnix    = "redis+unix"
	schemePostgresUnix = "postgres+unix"
)

// schemeHTTPUnix is the scheme of an http check through a Unix socket.
const schemeHTTPUnix = "http+unix"

// HTTPUnixHost is the host (so the Host header) of an `http+unix` URL.
const HTTPUnixHost = "localhost"

// parseHTTPUnix handles `http+unix:///run/app/app.sock:/health?x=1`: the part
// of the path before the first `:/` is the socket, the rest is the request.
func (p *ParsedURL) parseHTTPUnix(parsedURL *url.URL) error {
	if p.Scheme != schemeHTTPUnix {
		return errHTTPSUnix
	}

	if p.Host != "" || p.Port != 0 {
		return errUnixSocketHost
	}

	socket, request, found := strings.Cut(parsedURL.Path, ":/")
	if !found || socket == "" {
		return errHTTPUnixForm
	}

	p.Host = HTTPUnixHost
	p.Socket = socket
	p.Path = "/" + request
	p.Query = parsedURL.RawQuery

	return nil
}

// HTTPURL returns the http:// URL an `http+unix` URL stands for.
func (p *ParsedURL) HTTPURL() string {
	target := "http://" + p.Host + p.Path
	if p.Query != "" {
		target += "?" + p.Query
	}

	return target
}

// parseDNS handles DNS-specific URL parsing.
// Format: dns://resolver[:port]/domain[?type=A].
func (p *ParsedURL) parseDNS(parsedURL *url.URL) error {
	// Host is the DNS resolver (can be empty for system resolver)
	// Path is the domain to query
	domain := strings.TrimPrefix(parsedURL.Path, "/")
	if domain == "" {
		return errDNSRequiresDomain
	}

	p.DNSDomain = domain

	// Extract record type from query params (default: A)
	p.RecordType = strings.ToUpper(parsedURL.Query().Get("type"))
	if p.RecordType == "" {
		p.RecordType = "A"
	}

	if !validDNSRecordTypes[p.RecordType] {
		return fmt.Errorf("%w: %s", errInvalidDNSType, p.RecordType)
	}

	return nil
}

// InferCheckType returns the check type for a given URL.
// Returns empty string if type cannot be inferred.
func InferCheckType(rawURL string) checkerdef.CheckType {
	parsed, err := Parse(rawURL)
	if err != nil {
		return ""
	}

	return parsed.CheckType
}

// SuggestNameSlug generates name and slug from the parsed URL.
// Name format: "<target> (<type>)".
// Slug format: "<target-with-dashes>[-port]".
func (p *ParsedURL) SuggestNameSlug() (string, string) {
	target := p.Host

	// For DNS, use the domain being queried as the target
	if p.CheckType == checkerdef.CheckTypeDNS {
		target = p.DNSDomain
	}

	if p.Socket != "" {
		target = p.Socket
	}

	name := fmt.Sprintf("%s (%s)", target, p.CheckType)
	slug := strings.Trim(strings.NewReplacer(".", "-", "/", "-").Replace(target), "-")

	// Add port to slug if non-default
	if p.Port > 0 && !p.isDefaultPort() {
		slug = fmt.Sprintf("%s-%d", slug, p.Port)
	}

	return name, slug
}

// isDefaultPort returns true if the port is the default for the scheme.
func (p *ParsedURL) isDefaultPort() bool {
	//nolint:exhaustive // Only relevant check types have default ports.
	switch p.CheckType {
	case checkerdef.CheckTypeHTTP:
		if p.TLS {
			return p.Port == 443
		}

		return p.Port == 80
	case checkerdef.CheckTypeTCP:
		if p.TLS {
			return p.Port == 443
		}

		return false // TCP has no default port
	case checkerdef.CheckTypeDNS:
		return p.Port == 53 || p.Port == 0
	default:
		return true // ICMP doesn't use ports
	}
}

// Resolver returns the DNS resolver address (host:port format).
// Returns empty string if using system resolver.
func (p *ParsedURL) Resolver() string {
	if p.Host == "" {
		return ""
	}

	if p.Port > 0 && p.Port != 53 {
		return fmt.Sprintf("%s:%d", p.Host, p.Port)
	}

	return p.Host
}
