package checkdomain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// bootstrapURL is the IANA RDAP bootstrap registry that maps a TLD to the
// RDAP base URL(s) that answer for it.
const bootstrapURL = "https://data.iana.org/rdap/dns.json"

// bootstrapCacheTTL is how long the bootstrap document is cached in-process
// before being re-fetched, so per-check executions don't hit IANA every run.
const bootstrapCacheTTL = 24 * time.Hour

// rdapHTTPTimeout bounds both the bootstrap fetch and the domain query.
const rdapHTTPTimeout = 10 * time.Second

const rdapRoleRegistrar = "registrar"

const rdapEventActionExpiration = "expiration"

// errRDAPUnknownTLD is returned when the bootstrap document has no RDAP
// service registered for the domain's TLD.
var errRDAPUnknownTLD = errors.New("no RDAP service found for TLD")

// errRDAPNoExpirationEvent is returned when the RDAP domain response has no
// "expiration" event.
var errRDAPNoExpirationEvent = errors.New("no expiration event in RDAP response")

// rdapResult is the outcome of a successful RDAP lookup.
type rdapResult struct {
	ExpiryDate time.Time
	Registrar  string
}

// bootstrapDoc mirrors the shape of https://data.iana.org/rdap/dns.json:
//
//	{"services": [[["ac"], ["https://rdap.nic.ac/"]], ...]}
//
// Each service entry is a 2-element array: a list of TLDs, and a list of
// RDAP base URLs that serve them.
type bootstrapDoc struct {
	Services [][][]string `json:"services"`
}

// rdapEvent is one entry of a domain response's "events" array.
type rdapEvent struct {
	EventAction string `json:"eventAction"`
	EventDate   string `json:"eventDate"`
}

// rdapEntity is one entry of a domain response's "entities" array.
type rdapEntity struct {
	Roles      []string        `json:"roles"`
	VCardArray json.RawMessage `json:"vcardArray"`
}

// rdapDomainResponse is the subset of RFC 9083's domain object this package
// reads: expiration event and registrar entity.
type rdapDomainResponse struct {
	Events   []rdapEvent  `json:"events"`
	Entities []rdapEntity `json:"entities"`
}

// rdapClient looks up domain expiration/registrar data via RDAP (RFC
// 7480-7484), caching the IANA bootstrap document in-process.
type rdapClient struct {
	httpClient   *http.Client
	bootstrapURL string
	cacheTTL     time.Duration

	mu          sync.Mutex
	bootstrap   *bootstrapDoc
	bootstrapAt time.Time
}

// newRDAPClient builds a client pointed at the real IANA bootstrap registry.
func newRDAPClient() *rdapClient {
	return &rdapClient{
		httpClient:   &http.Client{Timeout: rdapHTTPTimeout},
		bootstrapURL: bootstrapURL,
		cacheTTL:     bootstrapCacheTTL,
	}
}

// defaultRDAPClient is the singleton used by production DomainChecker
// instances. It lives at package scope (not on DomainChecker) because
// registry.GetChecker constructs a fresh &DomainChecker{} on every call, so
// an instance-level cache would never actually be reused across executions.
//
//nolint:gochecknoglobals // deliberate process-lifetime cache, see comment above.
var defaultRDAPClient = newRDAPClient()

// lookup resolves the domain's expiration date and registrar via RDAP.
func (c *rdapClient) lookup(ctx context.Context, domain string) (*rdapResult, error) {
	tld := extractTLD(domain)

	base, err := c.baseURLForTLD(ctx, tld)
	if err != nil {
		return nil, err
	}

	return c.queryDomain(ctx, base, domain)
}

// extractTLD returns the last label of domain, lowercased, with any
// trailing root dot stripped.
func extractTLD(domain string) string {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))

	idx := strings.LastIndex(domain, ".")
	if idx < 0 {
		return domain
	}

	return domain[idx+1:]
}

// baseURLForTLD resolves tld to an RDAP base URL using the (cached)
// bootstrap document.
func (c *rdapClient) baseURLForTLD(ctx context.Context, tld string) (string, error) {
	doc, err := c.bootstrapDocument(ctx)
	if err != nil {
		return "", err
	}

	for _, entry := range doc.Services {
		if len(entry) < 2 { //nolint:mnd // a service entry is always [tlds, urls]
			continue
		}

		tlds, urls := entry[0], entry[1]
		if len(urls) == 0 {
			continue
		}

		for _, t := range tlds {
			if strings.EqualFold(t, tld) {
				return urls[0], nil
			}
		}
	}

	return "", fmt.Errorf("%w: %s", errRDAPUnknownTLD, tld)
}

// bootstrapDocument returns the cached bootstrap document, fetching a fresh
// one if the cache is empty or older than cacheTTL.
func (c *rdapClient) bootstrapDocument(ctx context.Context) (*bootstrapDoc, error) {
	c.mu.Lock()
	if c.bootstrap != nil && time.Since(c.bootstrapAt) < c.cacheTTL {
		doc := c.bootstrap
		c.mu.Unlock()

		return doc, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.bootstrapURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to build RDAP bootstrap request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch RDAP bootstrap document: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RDAP bootstrap fetch failed: status %d", resp.StatusCode) //nolint:err113 // dynamic status, not a sentinel
	}

	var doc bootstrapDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to decode RDAP bootstrap document: %w", err)
	}

	c.mu.Lock()
	c.bootstrap = &doc
	c.bootstrapAt = time.Now()
	c.mu.Unlock()

	return &doc, nil
}

// queryDomain performs GET {base}/domain/{domain} and extracts the
// expiration date and registrar name.
func (c *rdapClient) queryDomain(ctx context.Context, base, domain string) (*rdapResult, error) {
	url := strings.TrimRight(base, "/") + "/domain/" + domain

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to build RDAP domain request: %w", err)
	}

	req.Header.Set("Accept", "application/rdap+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("RDAP domain query failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RDAP domain query failed: status %d", resp.StatusCode) //nolint:err113 // dynamic status, not a sentinel
	}

	var doc rdapDomainResponse
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to decode RDAP domain response: %w", err)
	}

	return parseDomainResponse(&doc)
}

// parseDomainResponse extracts the expiration date and registrar from a
// decoded RDAP domain response.
func parseDomainResponse(doc *rdapDomainResponse) (*rdapResult, error) {
	var expiryStr string

	for _, e := range doc.Events {
		if e.EventAction == rdapEventActionExpiration {
			expiryStr = e.EventDate

			break
		}
	}

	if expiryStr == "" {
		return nil, errRDAPNoExpirationEvent
	}

	expiry, err := time.Parse(time.RFC3339, expiryStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse RDAP expiration date %q: %w", expiryStr, err)
	}

	var registrar string

	for _, ent := range doc.Entities {
		if hasRole(ent.Roles, rdapRoleRegistrar) {
			registrar = vcardFN(ent.VCardArray)

			break
		}
	}

	return &rdapResult{ExpiryDate: expiry, Registrar: registrar}, nil
}

func hasRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}

	return false
}

// vcardFN extracts the "fn" (formatted name) property from a jCard
// (RFC 7095) vCardArray, as used by RDAP entities:
//
//	["vcard", [["version", {}, "text", "4.0"], ["fn", {}, "text", "Example Registrar"]]]
//
// Returns "" if the array is malformed or has no "fn" property.
func vcardFN(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var arr []any
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) < 2 { //nolint:mnd // jCard is always [vcard, props]
		return ""
	}

	props, ok := arr[1].([]any)
	if !ok {
		return ""
	}

	const fnPropValueIndex = 3

	for _, p := range props {
		prop, ok := p.([]any)
		if !ok || len(prop) <= fnPropValueIndex {
			continue
		}

		name, _ := prop[0].(string)
		if name != "fn" {
			continue
		}

		if value, ok := prop[fnPropValueIndex].(string); ok {
			return value
		}
	}

	return ""
}
