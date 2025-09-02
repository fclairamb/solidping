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
		if p.Host == "" {
			return errMissingHost
		}

		p.Path = parsedURL.Path
		p.Query = parsedURL.RawQuery

	case checkerdef.CheckTypeTCP:
		if p.Host == "" {
			return errMissingHost
		}

		if p.Port == 0 {
			return errTCPRequiresPort
		}

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

	name := fmt.Sprintf("%s (%s)", target, p.CheckType)
	slug := strings.ReplaceAll(target, ".", "-")

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
