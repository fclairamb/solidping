# Unified URL Handling Specification

## Problem Statement

The current checker implementations have inconsistent configuration approaches:

| Checker | Target Field | Format Example |
|---------|-------------|----------------|
| HTTP | `url` | `https://example.com/path` |
| TCP | `host` + `port` | `example.com` + `443` |
| Ping | `host` | `example.com` |
| DNS | `host` | `example.com` |

This inconsistency creates several issues:
1. Users must know the exact configuration structure for each checker type
2. The `type` field is always required, even when the URL scheme makes it obvious
3. DNS checker doesn't follow the convention documentation (`checker_config_conventions.md`)
4. Auto-generation of name/slug from target only works for HTTP checks

## Goals

1. **Standardize URL field**: All checkers should accept a `url` field as the primary configuration
2. **Auto-detect check type**: Infer the checker type from the URL scheme
3. **Make type optional**: Remove the requirement to set `type` when creating a check with a URL
4. **Backward compatibility**: Continue supporting the legacy `host` fields

---

## URL Scheme Reference

| Scheme | Check Type | Example |
|--------|------------|---------|
| `http://`, `https://` | http | `https://google.com/search` |
| `tcp://` | tcp | `tcp://example.com:443` |
| `tcps://` | tcp (TLS) | `tcps://example.com:443` |
| `ping://` | ping | `ping://8.8.8.8` |
| `icmp://` | ping | `icmp://google.com` |
| `dns://` | dns | `dns://8.8.8.8/example.com` |

### URL Format Details

**HTTP/HTTPS:**
```
http[s]://<host>[:<port>][/<path>][?<query>]
```

**TCP:**
```
tcp[s]://<host>:<port>
```
- `tcp://` = plain TCP
- `tcps://` = TLS-secured TCP

**Ping:**
```
ping://<host>
icmp://<host>
```

**DNS:**
```
dns://<resolver>[:<port>]/<domain>[?type=<record_type>]
```

This format follows RFC 3986 and mirrors other URL patterns where the host is the server being monitored:

| Component | Meaning | Parallels |
|-----------|---------|-----------|
| `dns://` | Scheme | Like `http://`, `tcp://` |
| `resolver` | DNS server to query | The "host" being monitored |
| `:port` | Optional port (default 53) | Like `tcp://host:5432` |
| `/domain` | Domain to resolve | The "resource" we're requesting |
| `?type=` | Record type | Query parameter |

Query parameters:

| Param | Values | Default | Description |
|-------|--------|---------|-------------|
| `type` | `A`, `AAAA`, `MX`, `CNAME`, `TXT`, `NS`, `SOA`, `PTR` | `A` | Record type |

Examples:
- `dns://8.8.8.8/example.com` - A record query to Google DNS
- `dns://8.8.8.8/example.com?type=A` - Explicit A record
- `dns://1.1.1.1/example.com?type=MX` - MX record query to Cloudflare
- `dns://8.8.8.8:53/example.com?type=AAAA` - Custom port, IPv6 record
- `dns:///example.com` - System resolver (empty host), A record
- `dns:///example.com?type=MX` - System resolver, MX record

**Note:** When no resolver is specified (`dns:///domain`), the system's default DNS resolver is used.

### Complete URL Scheme Family

```
http://host/path                    # HTTP(S) monitoring
tcp://host:port                     # TCP port check
tcps://host:port                    # TCP with TLS
ping://host                         # Ping/ICMP (or icmp://)
dns://resolver/domain?type=A        # DNS query
```

---

## Implementation Tasks

### Task 1: Create URL Parser Package

**File:** `back/internal/checkers/urlparse/urlparse.go`

```go
package urlparse

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/back/internal/checkers/checkerdef"
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

// schemeMapping maps URL schemes to check types.
var schemeMapping = map[string]checkerdef.CheckType{
	"http":  checkerdef.CheckTypeHTTP,
	"https": checkerdef.CheckTypeHTTP,
	"tcp":   checkerdef.CheckTypeTCP,
	"tcps":  checkerdef.CheckTypeTCP,
	"ping":  checkerdef.CheckTypePing,
	"icmp":  checkerdef.CheckTypePing,
	"dns":   checkerdef.CheckTypeDNS,
}

// validDNSRecordTypes lists valid DNS record types.
var validDNSRecordTypes = map[string]bool{
	"A": true, "AAAA": true, "MX": true, "TXT": true,
	"CNAME": true, "NS": true, "SOA": true, "PTR": true,
}

// Parse analyzes a URL and extracts check configuration.
func Parse(rawURL string) (*ParsedURL, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("empty URL")
	}

	parsed := &ParsedURL{OriginalURL: rawURL}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	parsed.Scheme = strings.ToLower(u.Scheme)

	// Determine check type
	checkType, ok := schemeMapping[parsed.Scheme]
	if !ok {
		return nil, fmt.Errorf("unsupported scheme: %s", parsed.Scheme)
	}
	parsed.CheckType = checkType

	// Extract host and port
	parsed.Host = u.Hostname()
	if portStr := u.Port(); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid port: %s", portStr)
		}
		parsed.Port = port
	}

	// Set TLS based on scheme
	parsed.TLS = parsed.Scheme == "https" || parsed.Scheme == "tcps"

	// Handle scheme-specific parsing
	switch checkType {
	case checkerdef.CheckTypeHTTP:
		if parsed.Host == "" {
			return nil, fmt.Errorf("missing host in URL")
		}
		parsed.Path = u.Path
		parsed.Query = u.RawQuery

	case checkerdef.CheckTypeTCP:
		if parsed.Host == "" {
			return nil, fmt.Errorf("missing host in URL")
		}
		if parsed.Port == 0 {
			return nil, fmt.Errorf("TCP URL requires a port (e.g., tcp://host:port)")
		}

	case checkerdef.CheckTypePing:
		if parsed.Host == "" {
			return nil, fmt.Errorf("missing host in URL")
		}

	case checkerdef.CheckTypeDNS:
		if err := parsed.parseDNS(u); err != nil {
			return nil, err
		}
	}

	return parsed, nil
}

// parseDNS handles DNS-specific URL parsing.
// Format: dns://resolver[:port]/domain[?type=A]
func (p *ParsedURL) parseDNS(u *url.URL) error {
	// Host is the DNS resolver (can be empty for system resolver)
	// Path is the domain to query
	domain := strings.TrimPrefix(u.Path, "/")
	if domain == "" {
		return fmt.Errorf("DNS URL requires a domain in path (e.g., dns://8.8.8.8/example.com)")
	}
	p.DNSDomain = domain

	// Extract record type from query params (default: A)
	p.RecordType = strings.ToUpper(u.Query().Get("type"))
	if p.RecordType == "" {
		p.RecordType = "A"
	}
	if !validDNSRecordTypes[p.RecordType] {
		return fmt.Errorf("invalid DNS record type: %s", p.RecordType)
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
// Name format: "<target> (<type>)"
// Slug format: "<target-with-dashes>[-port]"
func (p *ParsedURL) SuggestNameSlug() (name, slug string) {
	target := p.Host

	// For DNS, use the domain being queried as the target
	if p.CheckType == checkerdef.CheckTypeDNS {
		target = p.DNSDomain
	}

	name = fmt.Sprintf("%s (%s)", target, p.CheckType)
	slug = strings.ReplaceAll(target, ".", "-")

	// Add port to slug if non-default
	if p.Port > 0 && !p.isDefaultPort() {
		slug = fmt.Sprintf("%s-%d", slug, p.Port)
	}

	return name, slug
}

// isDefaultPort returns true if the port is the default for the scheme.
func (p *ParsedURL) isDefaultPort() bool {
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
		return true // Ping doesn't use ports
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
```

**File:** `back/internal/checkers/urlparse/urlparse_test.go`

```go
package urlparse

import (
	"testing"

	"github.com/fclairamb/solidping/back/internal/checkers/checkerdef"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantType   checkerdef.CheckType
		wantHost   string
		wantPort   int
		wantTLS    bool
		wantRecord string
		wantDomain string
		wantErr    bool
	}{
		// HTTP
		{"http basic", "http://example.com", checkerdef.CheckTypeHTTP, "example.com", 0, false, "", "", false},
		{"https basic", "https://example.com", checkerdef.CheckTypeHTTP, "example.com", 0, true, "", "", false},
		{"https with port", "https://example.com:8443", checkerdef.CheckTypeHTTP, "example.com", 8443, true, "", "", false},
		{"https with path", "https://example.com/api/v1", checkerdef.CheckTypeHTTP, "example.com", 0, true, "", "", false},

		// TCP
		{"tcp basic", "tcp://example.com:3306", checkerdef.CheckTypeTCP, "example.com", 3306, false, "", "", false},
		{"tcps basic", "tcps://example.com:443", checkerdef.CheckTypeTCP, "example.com", 443, true, "", "", false},
		{"tcp no port", "tcp://example.com", "", "", 0, false, "", "", true},

		// Ping
		{"ping basic", "ping://8.8.8.8", checkerdef.CheckTypePing, "8.8.8.8", 0, false, "", "", false},
		{"icmp basic", "icmp://google.com", checkerdef.CheckTypePing, "google.com", 0, false, "", "", false},

		// DNS - new format: dns://resolver/domain?type=X
		{"dns with resolver", "dns://8.8.8.8/example.com", checkerdef.CheckTypeDNS, "8.8.8.8", 0, false, "A", "example.com", false},
		{"dns with type", "dns://8.8.8.8/example.com?type=MX", checkerdef.CheckTypeDNS, "8.8.8.8", 0, false, "MX", "example.com", false},
		{"dns with port", "dns://8.8.8.8:53/example.com?type=AAAA", checkerdef.CheckTypeDNS, "8.8.8.8", 53, false, "AAAA", "example.com", false},
		{"dns system resolver", "dns:///example.com", checkerdef.CheckTypeDNS, "", 0, false, "A", "example.com", false},
		{"dns system resolver MX", "dns:///example.com?type=MX", checkerdef.CheckTypeDNS, "", 0, false, "MX", "example.com", false},
		{"dns cloudflare", "dns://1.1.1.1/google.com?type=TXT", checkerdef.CheckTypeDNS, "1.1.1.1", 0, false, "TXT", "google.com", false},
		{"dns hostname resolver", "dns://dns.google/example.com", checkerdef.CheckTypeDNS, "dns.google", 0, false, "A", "example.com", false},

		// Errors
		{"empty", "", "", "", 0, false, "", "", true},
		{"invalid scheme", "ftp://example.com", "", "", 0, false, "", "", true},
		{"no host http", "https://", "", "", 0, false, "", "", true},
		{"dns no domain", "dns://8.8.8.8/", "", "", 0, false, "", "", true},
		{"dns invalid type", "dns://8.8.8.8/example.com?type=INVALID", "", "", 0, false, "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			parsed, err := Parse(tt.url)

			if tt.wantErr {
				r.Error(err)
				return
			}

			r.NoError(err)
			r.Equal(tt.wantType, parsed.CheckType)
			r.Equal(tt.wantHost, parsed.Host)
			r.Equal(tt.wantPort, parsed.Port)
			r.Equal(tt.wantTLS, parsed.TLS)
			r.Equal(tt.wantRecord, parsed.RecordType)
			r.Equal(tt.wantDomain, parsed.DNSDomain)
		})
	}
}

func TestSuggestNameSlug(t *testing.T) {
	tests := []struct {
		url      string
		wantName string
		wantSlug string
	}{
		{"https://google.com", "google.com (http)", "google-com"},
		{"tcp://db.example.com:3306", "db.example.com (tcp)", "db-example-com-3306"},
		{"tcps://db.example.com:443", "db.example.com (tcp)", "db-example-com"},
		{"ping://8.8.8.8", "8.8.8.8 (ping)", "8-8-8-8"},
		// DNS uses domain (not resolver) for name/slug
		{"dns://8.8.8.8/google.com", "google.com (dns)", "google-com"},
		{"dns://1.1.1.1/example.com?type=MX", "example.com (dns)", "example-com"},
		{"dns:///example.com", "example.com (dns)", "example-com"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			r := require.New(t)
			parsed, err := Parse(tt.url)
			r.NoError(err)

			name, slug := parsed.SuggestNameSlug()
			r.Equal(tt.wantName, name)
			r.Equal(tt.wantSlug, slug)
		})
	}
}

func TestResolver(t *testing.T) {
	tests := []struct {
		url          string
		wantResolver string
	}{
		{"dns://8.8.8.8/example.com", "8.8.8.8"},
		{"dns://8.8.8.8:53/example.com", "8.8.8.8"},
		{"dns://8.8.8.8:5353/example.com", "8.8.8.8:5353"},
		{"dns:///example.com", ""},
		{"dns://dns.google/example.com", "dns.google"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			r := require.New(t)
			parsed, err := Parse(tt.url)
			r.NoError(err)
			r.Equal(tt.wantResolver, parsed.Resolver())
		})
	}
}
```

---

### Task 2: Update TCP Config

**File:** `back/internal/checkers/checktcp/config.go`

Add URL field and update FromMap:

```go
type TCPConfig struct {
	URL           string        `json:"url,omitempty"`
	Host          string        `json:"host,omitempty"`
	Port          int           `json:"port,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`
	SendData      string        `json:"send_data,omitempty"`
	ExpectData    string        `json:"expect_data,omitempty"`
	TLS           bool          `json:"tls,omitempty"`
	TLSVerify     bool          `json:"tls_verify,omitempty"`
	TLSServerName string        `json:"tls_server_name,omitempty"`
}

func (c *TCPConfig) FromMap(configMap map[string]any) error {
	// URL takes precedence if provided
	if urlStr, ok := configMap["url"].(string); ok && urlStr != "" {
		c.URL = urlStr
		parsed, err := urlparse.Parse(urlStr)
		if err != nil {
			return checkerdef.NewConfigError("url", err.Error())
		}
		if parsed.CheckType != checkerdef.CheckTypeTCP {
			return checkerdef.NewConfigError("url", "must be a TCP URL (tcp:// or tcps://)")
		}
		c.Host = parsed.Host
		c.Port = parsed.Port
		c.TLS = parsed.TLS
	} else {
		// Fall back to legacy host+port
		if host, ok := configMap["host"].(string); ok {
			c.Host = host
		}
		// ... existing port parsing ...
	}
	// ... rest of existing parsing (TLSVerify, SendData, etc.) ...
}
```

---

### Task 3: Update Ping Config

**File:** `back/internal/checkers/checkping/config.go`

```go
type PingConfig struct {
	URL        string        `json:"url,omitempty"`
	Host       string        `json:"host,omitempty"`
	Timeout    time.Duration `json:"timeout,omitempty"`
	Count      int           `json:"count,omitempty"`
	Interval   time.Duration `json:"interval,omitempty"`
	PacketSize int           `json:"packet_size,omitempty"`
	TTL        int           `json:"ttl,omitempty"`
}

func (c *PingConfig) FromMap(configMap map[string]any) error {
	// URL takes precedence if provided
	if urlStr, ok := configMap["url"].(string); ok && urlStr != "" {
		c.URL = urlStr
		parsed, err := urlparse.Parse(urlStr)
		if err != nil {
			return checkerdef.NewConfigError("url", err.Error())
		}
		if parsed.CheckType != checkerdef.CheckTypePing {
			return checkerdef.NewConfigError("url", "must be a ping URL (ping:// or icmp://)")
		}
		c.Host = parsed.Host
	} else {
		// Fall back to legacy host
		if host, ok := configMap["host"].(string); ok {
			c.Host = host
		}
	}
	// ... rest of existing parsing ...
}
```

---

### Task 4: Update DNS Config

**File:** `back/internal/checkers/checkdns/config.go`

Rename `Hostname` to `Host` and add URL support with new format:

```go
type DNSConfig struct {
	URL            string        `json:"url,omitempty"`
	Host           string        `json:"host,omitempty"`   // Domain to query (renamed from Hostname)
	Timeout        time.Duration `json:"timeout,omitempty"`
	Nameserver     string        `json:"nameserver,omitempty"`
	RecordType     string        `json:"record_type,omitempty"`
	ExpectedIPs    []string      `json:"expected_ips,omitempty"`
	ExpectedValues []string      `json:"expected_values,omitempty"`
}

func (c *DNSConfig) FromMap(configMap map[string]any) error {
	// URL takes precedence if provided
	// Format: dns://resolver/domain?type=A
	if urlStr, ok := configMap["url"].(string); ok && urlStr != "" {
		c.URL = urlStr
		parsed, err := urlparse.Parse(urlStr)
		if err != nil {
			return checkerdef.NewConfigError("url", err.Error())
		}
		if parsed.CheckType != checkerdef.CheckTypeDNS {
			return checkerdef.NewConfigError("url", "must be a DNS URL (dns://)")
		}
		// Domain to query comes from the path
		c.Host = parsed.DNSDomain
		c.RecordType = parsed.RecordType
		// Resolver comes from the host (empty = system resolver)
		if parsed.Resolver() != "" {
			c.Nameserver = parsed.Resolver()
		}
	} else {
		// Fall back to legacy host/hostname
		if host, ok := configMap["host"].(string); ok {
			c.Host = host
		} else if hostname, ok := configMap["hostname"].(string); ok {
			// Backward compatibility with old field name
			c.Host = hostname
		}
	}
	// ... rest of existing parsing ...
}
```

Also update the checker.go to use `c.Host` instead of `c.Hostname`.

---

### Task 5: Add Type Inference to Registry

**File:** `back/internal/checkers/registry/registry.go`

```go
import "github.com/fclairamb/solidping/back/internal/checkers/urlparse"

// InferCheckType returns the check type for a given URL.
// Returns empty CheckType if type cannot be inferred.
func InferCheckType(urlStr string) checkerdef.CheckType {
	return urlparse.InferCheckType(urlStr)
}

// InferCheckTypeFromConfig examines a config map and infers the check type.
// Returns empty CheckType if type cannot be inferred.
func InferCheckTypeFromConfig(config map[string]any) checkerdef.CheckType {
	if url, ok := config["url"].(string); ok && url != "" {
		return InferCheckType(url)
	}
	return ""
}
```

---

### Task 6: Update Check Creation Handler

**File:** `back/internal/handlers/checks/handler.go`

Modify the Create handler to infer type when not provided:

```go
func (h *ChecksHandler) Create(w http.ResponseWriter, r *http.Request) {
	// ... existing code to parse request body ...

	// Infer type from URL if not specified
	if input.Type == "" {
		inferredType := registry.InferCheckTypeFromConfig(input.Config)
		if inferredType == "" {
			h.WriteError(w, http.StatusBadRequest, base.ErrorCodeValidation,
				"type is required when url is not provided or has unrecognized scheme")
			return
		}
		input.Type = string(inferredType)
	}

	// Auto-generate name and slug if not provided
	if input.Name == "" || input.Slug == "" {
		if urlStr, ok := input.Config["url"].(string); ok && urlStr != "" {
			parsed, err := urlparse.Parse(urlStr)
			if err == nil {
				name, slug := parsed.SuggestNameSlug()
				if input.Name == "" {
					input.Name = name
				}
				if input.Slug == "" {
					input.Slug = slug
				}
			}
		}
	}

	// ... continue with existing creation logic ...
}
```

---

### Task 7: Update Documentation

**File:** `agent_docs/checker_config_conventions.md`

Update to reflect new URL-based approach:

```markdown
## Standard Configuration

All checkers support the `url` field as the primary configuration. The URL scheme determines the check type automatically.

### URL Schemes

| Scheme | Check Type | Example |
|--------|------------|---------|
| `http://`, `https://` | http | `https://example.com` |
| `tcp://`, `tcps://` | tcp | `tcp://example.com:3306` |
| `ping://`, `icmp://` | ping | `ping://8.8.8.8` |
| `dns://` | dns | `dns://8.8.8.8/example.com` |

### Common Options
- `url`: The target URL (scheme determines check type)
- `timeout`: The timeout duration for the check

## Per-Checker Configuration

### For `http`:
- `url`: Required. HTTP or HTTPS URL
- ... (rest of existing docs)

### For `tcp`:
- `url`: TCP URL (e.g., `tcp://host:port`, `tcps://host:port`)
- Or legacy: `host` + `port`
- ... (rest of existing docs)

### For `ping`:
- `url`: Ping URL (e.g., `ping://host`, `icmp://host`)
- Or legacy: `host`

### For `dns`:
- `url`: DNS URL format: `dns://resolver/domain?type=A`
  - `dns://8.8.8.8/example.com` - A record via Google DNS
  - `dns://1.1.1.1/example.com?type=MX` - MX record via Cloudflare
  - `dns:///example.com` - A record using system resolver
- Or legacy: `host`, `record_type`, `nameserver`
```

---

## File Changes Summary

| File | Changes |
|------|---------|
| `checkers/urlparse/urlparse.go` | **New**: URL parsing and type inference |
| `checkers/urlparse/urlparse_test.go` | **New**: Comprehensive tests |
| `checkers/checktcp/config.go` | Add `URL` field, update `FromMap` to parse URL |
| `checkers/checktcp/checker.go` | No changes needed |
| `checkers/checkping/config.go` | Add `URL` field, update `FromMap` to parse URL |
| `checkers/checkping/checker.go` | No changes needed |
| `checkers/checkdns/config.go` | Rename `Hostname` to `Host`, add `URL` field |
| `checkers/checkdns/checker.go` | Update to use `Host` instead of `Hostname` |
| `checkers/registry/registry.go` | Add `InferCheckType` and `InferCheckTypeFromConfig` |
| `handlers/checks/handler.go` | Make `type` optional, auto-generate name/slug |
| `agent_docs/checker_config_conventions.md` | Update documentation |

---

## API Examples

### Minimal Creation (type, name, slug inferred)

```json
POST /api/v1/orgs/default/checks
{"config": {"url": "https://google.com"}}
```

Response:
```json
{
  "uid": "...",
  "name": "google.com (http)",
  "slug": "google-com",
  "type": "http",
  "config": {"url": "https://google.com"}
}
```

### TCP with TLS

```json
{"config": {"url": "tcps://db.example.com:5432"}}
```

Response:
```json
{
  "name": "db.example.com (tcp)",
  "slug": "db-example-com-5432",
  "type": "tcp",
  "config": {
    "url": "tcps://db.example.com:5432",
    "host": "db.example.com",
    "port": 5432,
    "tls": true
  }
}
```

### DNS with custom resolver

```json
{"config": {"url": "dns://8.8.8.8/google.com?type=MX"}}
```

Response:
```json
{
  "name": "google.com (dns)",
  "slug": "google-com",
  "type": "dns",
  "config": {
    "url": "dns://8.8.8.8/google.com?type=MX",
    "host": "google.com",
    "record_type": "MX",
    "nameserver": "8.8.8.8"
  }
}
```

### DNS with system resolver

```json
{"config": {"url": "dns:///example.com"}}
```

Response:
```json
{
  "name": "example.com (dns)",
  "slug": "example-com",
  "type": "dns",
  "config": {
    "url": "dns:///example.com",
    "host": "example.com",
    "record_type": "A"
  }
}
```

### Explicit (legacy still works)

```json
{
  "name": "My Database",
  "slug": "my-database",
  "type": "tcp",
  "config": {"host": "db.example.com", "port": 3306}
}
```

---

## Testing Checklist

- [ ] urlparse: Parse all supported schemes (http, https, tcp, tcps, ping, icmp, dns)
- [ ] urlparse: Handle invalid URLs gracefully
- [ ] urlparse: SuggestNameSlug generates correct output
- [ ] urlparse: DNS parses resolver from host, domain from path
- [ ] urlparse: DNS parses record type from `?type=` param
- [ ] urlparse: DNS handles empty host (system resolver)
- [ ] urlparse: Resolver() returns correct format with custom port
- [ ] TCP config: Accept both URL and host+port
- [ ] Ping config: Accept both URL and host
- [ ] DNS config: Accept URL, host (but not hostname)
- [ ] DNS config: Parse domain, record type, resolver from URL
- [ ] Registry: InferCheckType returns correct types
- [ ] Handler: Create check with URL only (infer type, name, slug)
- [ ] Handler: Create check with explicit type (URL parsing still works)
- [ ] Handler: Create check with legacy fields (backward compat)
- [ ] Handler: Error when type cannot be inferred

---

## Backward Compatibility

1. **Type field**: Still accepted and takes precedence
2. **Legacy fields**: `host`, `port`, continue to work
3. **Mixed usage**: If both `url` and legacy fields provided, `url` takes precedence
4. **Existing checks**: No migration needed
