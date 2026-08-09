package importers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// DefaultBetterStackBaseURL is the public Better Stack (Uptime) API root.
const DefaultBetterStackBaseURL = "https://uptime.betterstack.com"

// betterStackMaxPages bounds pagination so a misbehaving (or hostile) API
// cannot keep us fetching forever.
const betterStackMaxPages = 200

// betterStackTimeout bounds the whole monitors+heartbeats fetch.
const betterStackTimeout = 60 * time.Second

var (
	// ErrBetterStackTokenRequired is returned when the request body carries no token.
	ErrBetterStackTokenRequired = errors.New("a Better Stack API token is required")
	// ErrBetterStackUnauthorized is returned when Better Stack rejects the token.
	ErrBetterStackUnauthorized = errors.New("the Better Stack API rejected the token")
	// ErrBetterStackAPI is returned for any other non-success API response.
	ErrBetterStackAPI = errors.New("the Better Stack API returned an error")
	// ErrBetterStackUnreachable is returned when the API could not be reached.
	ErrBetterStackUnreachable = errors.New("the Better Stack API could not be reached")
)

// BetterStackOptions configures the converter. BaseURL is overridable so tests
// (and self-hosted proxies) can point at another endpoint; the token is never
// part of the options because it is never stored — it arrives with the request
// body and lives only for the duration of one Convert call.
type BetterStackOptions struct {
	BaseURL string
	Client  *http.Client
}

// BetterStackConverter fetches monitors and heartbeats from the Better Stack
// REST API and converts them into an ExportDocument.
//
// The API token is supplied in the request payload, used only as a Bearer
// credential on the outbound requests, and never persisted, logged, or echoed
// back in an error message.
type BetterStackConverter struct {
	baseURL string
	client  *http.Client
}

// NewBetterStackConverter builds a converter with the supplied options.
func NewBetterStackConverter(opts BetterStackOptions) *BetterStackConverter {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = DefaultBetterStackBaseURL
	}

	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: betterStackTimeout}
	}

	return &BetterStackConverter{baseURL: base, client: client}
}

// Source implements Converter.
func (c *BetterStackConverter) Source() string { return SourceBetterStack }

// betterStackRequest is the JSON body the convert endpoint accepts for this
// source. The token is read into memory for the duration of the call only.
type betterStackRequest struct {
	Token   string `json:"token"`
	BaseURL string `json:"baseUrl,omitempty"`
}

// betterStackPage is one page of a Better Stack collection response.
type betterStackPage struct {
	Data       []betterStackItem `json:"data"`
	Pagination struct {
		Next string `json:"next"`
	} `json:"pagination"`
}

// betterStackItem is one monitor or heartbeat.
type betterStackItem struct {
	ID         string          `json:"id"`
	Attributes json.RawMessage `json:"attributes"`
}

// betterStackMonitor is the subset of monitor attributes we map.
//
//nolint:tagliatelle // field names are Better Stack's, not ours
type betterStackMonitor struct {
	URL               string `json:"url"`
	PronounceableName string `json:"pronounceable_name"`
	MonitorType       string `json:"monitor_type"`
	CheckFrequency    int    `json:"check_frequency"`
	RequestTimeout    int    `json:"request_timeout"`
	HTTPMethod        string `json:"http_method"`
	RequestBody       string `json:"request_body"`
	RequestHeaders    []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"request_headers"`
	RequiredKeyword     string `json:"required_keyword"`
	ExpectedStatusCodes []int  `json:"expected_status_codes"`
	Port                any    `json:"port"`
	Paused              bool   `json:"paused"`
	VerifySSL           *bool  `json:"verify_ssl"`
	FollowRedirects     *bool  `json:"follow_redirects"`
	AuthUsername        string `json:"auth_username"`
	AuthPassword        string `json:"auth_password"`
	IPVersion           string `json:"ip_version"`
	MonitorGroupID      any    `json:"monitor_group_id"`
	SSLExpiration       any    `json:"ssl_expiration"`
	DomainExpiration    any    `json:"domain_expiration"`
}

// betterStackHeartbeat is the subset of heartbeat attributes we map.
type betterStackHeartbeat struct {
	Name   string `json:"name"`
	Period int    `json:"period"`
	Grace  int    `json:"grace"`
	Paused bool   `json:"paused"`
}

// Convert implements Converter using a background context.
func (c *BetterStackConverter) Convert(input []byte) (*ConversionResult, error) {
	return c.ConvertContext(context.Background(), input)
}

// ConvertContext parses the request body, fetches every page of monitors and
// heartbeats, and converts them.
func (c *BetterStackConverter) ConvertContext(ctx context.Context, input []byte) (*ConversionResult, error) {
	var req betterStackRequest
	if err := json.Unmarshal(input, &req); err != nil {
		return nil, fmt.Errorf("parse better stack request: %w", err)
	}

	token := strings.TrimSpace(req.Token)
	if token == "" {
		return nil, ErrBetterStackTokenRequired
	}

	converter := c
	if base := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"); base != "" && base != c.baseURL {
		converter = NewBetterStackConverter(BetterStackOptions{BaseURL: base, Client: c.client})
	}

	monitors, err := converter.fetchAll(ctx, token, "/api/v2/monitors")
	if err != nil {
		return nil, err
	}

	heartbeats, err := converter.fetchAll(ctx, token, "/api/v2/heartbeats")
	if err != nil {
		return nil, err
	}

	if len(monitors)+len(heartbeats) == 0 {
		return nil, fmt.Errorf("%w: the Better Stack account has no monitors or heartbeats", ErrEmptyInput)
	}

	warn := &warnings{}
	doc := newDocument(SourceBetterStack)
	slugs := newSlugSet()

	bothFamilies := 0

	for i := range monitors {
		check, ok, usedBothFamilies := convertBetterStackMonitor(&monitors[i], slugs, warn)
		if !ok {
			continue
		}

		doc.Checks = append(doc.Checks, check)

		if usedBothFamilies {
			bothFamilies++
		}
	}

	warnBetterStackBothFamilies(bothFamilies, warn)

	for i := range heartbeats {
		if check, ok := convertBetterStackHeartbeat(&heartbeats[i], slugs, warn); ok {
			doc.Checks = append(doc.Checks, check)
		}
	}

	if len(heartbeats) > 0 {
		warn.addDocf("%d heartbeat(s) were imported: SolidPing issues its own ping URLs, so repoint every "+
			"cron job or agent at the new URL after the import", len(heartbeats))
	}

	if len(doc.Checks) == 0 {
		return nil, fmt.Errorf("%w: no Better Stack monitor could be mapped to a SolidPing check", ErrEmptyInput)
	}

	normalizeChecks(doc, warn)

	return &ConversionResult{Document: doc, Warnings: warn.result()}, nil
}

// fetchAll walks every page of a Better Stack collection. The token is only
// ever sent as an Authorization header; it never appears in a URL, an error, or
// a log line.
func (c *BetterStackConverter) fetchAll(
	ctx context.Context, token, path string,
) ([]betterStackItem, error) {
	next := c.baseURL + path
	items := make([]betterStackItem, 0)

	for page := 0; page < betterStackMaxPages && next != ""; page++ {
		body, err := c.get(ctx, token, next)
		if err != nil {
			return nil, err
		}

		var decoded betterStackPage
		if decodeErr := json.Unmarshal(body, &decoded); decodeErr != nil {
			return nil, fmt.Errorf("%w: unreadable response for %s", ErrBetterStackAPI, path)
		}

		items = append(items, decoded.Data...)

		next, err = c.nextPageURL(decoded.Pagination.Next)
		if err != nil {
			return nil, err
		}
	}

	return items, nil
}

// nextPageURL validates the pagination cursor: it must stay on the configured
// API host, so a hostile or misconfigured response cannot redirect the
// credential-bearing request to another server.
func (c *BetterStackConverter) nextPageURL(next string) (string, error) {
	if next == "" {
		return "", nil
	}

	parsed, err := url.Parse(next)
	if err != nil {
		return "", fmt.Errorf("%w: invalid pagination cursor", ErrBetterStackAPI)
	}

	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("%w: invalid base url", ErrBetterStackAPI)
	}

	if parsed.Host != base.Host || parsed.Scheme != base.Scheme {
		return "", fmt.Errorf("%w: pagination cursor points at a different host", ErrBetterStackAPI)
	}

	return next, nil
}

// get performs one authenticated GET and returns the raw body.
func (c *BetterStackConverter) get(ctx context.Context, token, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBetterStackUnreachable, endpoint)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		// The error is deliberately not wrapped: transport errors can embed the
		// request URL, and we never want a credential-bearing request echoed
		// back to the caller.
		return nil, fmt.Errorf("%w (%s)", ErrBetterStackUnreachable, endpoint)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: could not read the response body", ErrBetterStackUnreachable)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w (HTTP %d)", ErrBetterStackUnauthorized, resp.StatusCode)
	case resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices:
		// The response body is intentionally NOT included: it can echo the
		// request (and therefore the token) back at us.
		return nil, fmt.Errorf("%w (HTTP %d)", ErrBetterStackAPI, resp.StatusCode)
	}

	return body, nil
}

// convertBetterStackMonitor maps one monitor onto a check.
//
// The third return value reports whether the monitor relied on Better Stack's
// unset ip_version (which there means "both families") on a type SolidPing can
// pin — the divergence Convert reports once for the whole import.
//
//nolint:funlen // one dispatch per Better Stack monitor type
func convertBetterStackMonitor(
	item *betterStackItem, slugs *slugSet, warn *warnings,
) (checks.ExportCheck, bool, bool) {
	var monitor betterStackMonitor
	if err := json.Unmarshal(item.Attributes, &monitor); err != nil {
		warn.addf("monitor "+item.ID, "", "monitor skipped: its attributes could not be decoded")

		return checks.ExportCheck{}, false, false
	}

	name := monitor.PronounceableName
	if name == "" {
		name = monitor.URL
	}

	check := checks.ExportCheck{
		Name:    name,
		Slug:    slugs.unique(name),
		Enabled: !monitor.Paused,
		Period:  secondsToPeriod(monitor.CheckFrequency),
		Config:  map[string]any{},
	}

	host := betterStackHost(monitor.URL)
	port := betterStackPort(monitor.Port)
	timeout := secondsToPeriod(monitor.RequestTimeout)

	switch monitor.MonitorType {
	case "status", "expected_status_code", srcKeyword, "keyword_absence":
		check.Type = string(checkerdef.CheckTypeHTTP)
		check.Config = betterStackHTTPConfig(&monitor, timeout, name, warn)
	case srcPing:
		check.Type = string(checkerdef.CheckTypeICMP)
		check.Config = map[string]any{cfgKeyHost: host}
	case srcTCP:
		check.Type = string(checkerdef.CheckTypeTCP)
		check.Config = betterStackHostPort(host, port, timeout)
	case "udp":
		check.Type = string(checkerdef.CheckTypeUDP)
		check.Config = betterStackHostPort(host, port, timeout)
	case "smtp":
		check.Type = string(checkerdef.CheckTypeSMTP)
		check.Config = betterStackHostPort(host, port, timeout)
	case "pop":
		check.Type = string(checkerdef.CheckTypePOP3)
		check.Config = betterStackHostPort(host, port, timeout)
	case "imap":
		check.Type = string(checkerdef.CheckTypeIMAP)
		check.Config = betterStackHostPort(host, port, timeout)
	case "dns":
		check.Type = string(checkerdef.CheckTypeDNS)
		check.Config = map[string]any{cfgKeyHost: host}
	default:
		warn.addf(name, "monitor_type",
			"monitor skipped: Better Stack type %q has no SolidPing counterpart", monitor.MonitorType)

		return checks.ExportCheck{}, false, false
	}

	betterStackWarnUnmapped(&monitor, name, warn)
	betterStackApplyIPVersion(&monitor, &check, name, warn)

	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckType(check.Type))
	bothFamilies := meta != nil && meta.SupportsIPVersion && betterStackMonitorUsesBothFamilies(&monitor)

	return check, true, bothFamilies
}

// betterStackApplyIPVersion carries Better Stack's per-monitor `ip_version`
// across — and is deliberately loud about the one place the two products
// disagree.
//
// Better Stack's tri-state is ipv4 / ipv6 / null, where NULL MEANS "USE BOTH
// FAMILIES". SolidPing's `auto` means "pick one, exactly as before" — probing
// both families needs two probes and a rollup status, which is a separate
// feature. Mapping null onto auto silently would therefore quietly halve the
// coverage of every monitor that relied on the default, so the unset case maps
// to auto AND is reported (see the document-level warning in Convert).
//
// An explicitly pinned family is carried through when the mapped SolidPing type
// can honor it, and warned about when it cannot (icmp/dns/... via the metadata
// flag), rather than being written into a config the API would reject.
func betterStackApplyIPVersion(
	monitor *betterStackMonitor, check *checks.ExportCheck, name string, warn *warnings,
) {
	version, err := checkerdef.ParseIPVersion(monitor.IPVersion)
	if err != nil {
		warn.addf(name, betterStackIPVersionField,
			"Better Stack ip_version %q is not a value SolidPing understands — the check will "+
				"monitor whichever family the target resolves to first", monitor.IPVersion)

		return
	}

	if !version.Explicit() {
		return
	}

	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckType(check.Type))
	if meta == nil || !meta.SupportsIPVersion {
		warn.addf(name, betterStackIPVersionField,
			"Better Stack pinned this monitor to %s, but SolidPing %q checks cannot be pinned to an "+
				"IP version — the check will monitor whichever family the target resolves to first",
			version.Label(), check.Type)

		return
	}

	if check.Config == nil {
		check.Config = map[string]any{}
	}

	check.Config[checkerdef.IPVersionConfigKey] = string(version)
}

// warnBetterStackBothFamilies states the one knowing divergence from Better
// Stack up front rather than letting it be discovered later: their unset
// ip_version monitors BOTH families, ours monitors one. Reported once for the
// whole import instead of once per monitor, since it is the Better Stack default
// and would otherwise drown out every other warning.
func warnBetterStackBothFamilies(count int, warn *warnings) {
	if count == 0 {
		return
	}

	warn.addDocf("%d monitor(s) left Better Stack's ip_version unset, which there means "+
		"\"monitor over both IPv4 and IPv6\". SolidPing checks one family per check: these were "+
		"imported as ipVersion: auto and will only be monitored over whichever family the target "+
		"resolves to first. Create a second check with ipVersion: ipv6 for the targets where IPv6 "+
		"reachability matters", count)
}

// betterStackMonitorUsesBothFamilies reports whether a monitor relies on Better
// Stack's null/unset ip_version — the value that means "monitor both families",
// which SolidPing cannot reproduce in a single check.
func betterStackMonitorUsesBothFamilies(monitor *betterStackMonitor) bool {
	version, err := checkerdef.ParseIPVersion(monitor.IPVersion)

	return err == nil && !version.Explicit()
}

// betterStackIPVersionField is the Better Stack attribute name reported on
// ip_version warnings, so the warning points at the source field.
const betterStackIPVersionField = "ip_version"

// betterStackWarnUnmapped reports monitor settings with no SolidPing counterpart.
func betterStackWarnUnmapped(monitor *betterStackMonitor, name string, warn *warnings) {
	if monitor.AuthUsername != "" || monitor.AuthPassword != "" {
		// checkhttp.HTTPConfig.BasicAuth would hold these; they are dropped as
		// a deliberate security policy, not for lack of a field.
		warn.addf(name, "auth_password",
			"basic-auth credentials were deliberately not imported (SolidPing never re-persists secrets "+
				"from a foreign export) — re-enter them on the check")
	}

	if monitor.MonitorGroupID != nil {
		warn.addf(name, "monitor_group_id",
			"Better Stack monitor groups are not imported — assign a SolidPing check group manually")
	}

	if monitor.SSLExpiration != nil {
		warn.addf(name, "ssl_expiration",
			"certificate expiry is a dedicated ssl check in SolidPing — add one for this host")
	}

	if monitor.DomainExpiration != nil {
		warn.addf(name, "domain_expiration",
			"domain expiry is a dedicated domain check in SolidPing — add one for this host")
	}
}

// betterStackHTTPConfig builds the http checker config.
func betterStackHTTPConfig(
	monitor *betterStackMonitor, timeout, name string, warn *warnings,
) map[string]any {
	cfg := map[string]any{cfgKeyURL: monitor.URL}

	if monitor.HTTPMethod != "" {
		cfg["method"] = strings.ToUpper(monitor.HTTPMethod)
	}

	betterStackApplyClientOptions(monitor, cfg)

	if monitor.RequestBody != "" {
		cfg["body"] = monitor.RequestBody
	}

	if timeout != "" {
		cfg[cfgKeyTimeout] = timeout
	}

	if len(monitor.ExpectedStatusCodes) > 0 {
		codes := make([]any, 0, len(monitor.ExpectedStatusCodes))
		for _, code := range monitor.ExpectedStatusCodes {
			codes = append(codes, strconv.Itoa(code))
		}

		cfg["expected_status_codes"] = codes
	}

	if monitor.RequiredKeyword != "" {
		if monitor.MonitorType == "keyword_absence" {
			cfg["body_reject"] = monitor.RequiredKeyword
		} else {
			cfg["body_expect"] = monitor.RequiredKeyword
		}
	}

	headers := map[string]any{}
	secretHeaders := map[string]any{}

	for i := range monitor.RequestHeaders {
		header := &monitor.RequestHeaders[i]
		if header.Name == "" {
			continue
		}

		if betterStackIsSecretHeader(header.Name) {
			secretHeaders[header.Name] = header.Value

			continue
		}

		headers[header.Name] = header.Value
	}

	if len(headers) > 0 {
		cfg["headers"] = headers
	}

	if len(secretHeaders) > 0 {
		cfg["secretHeaders"] = secretHeaders

		warn.addf(name, "request_headers",
			"%d credential-bearing header(s) were imported into the encrypted secretHeaders field",
			len(secretHeaders))
	}

	return cfg
}

// betterStackApplyClientOptions maps Better Stack's verify_ssl / follow_redirects
// onto the http checker's verifySsl / followRedirects, mutating cfg in place.
func betterStackApplyClientOptions(monitor *betterStackMonitor, cfg map[string]any) {
	if monitor.VerifySSL != nil && !*monitor.VerifySSL {
		cfg["verifySsl"] = false
	}

	if monitor.FollowRedirects != nil && !*monitor.FollowRedirects {
		cfg["followRedirects"] = false
	}
}

// betterStackIsSecretHeader reports whether a header name looks credential-bearing.
func betterStackIsSecretHeader(name string) bool {
	// Header names whose values belong in the encrypted secretHeaders map
	// rather than in the queryable public config.
	secretNames := []string{"authorization", "cookie", "token", "secret", "key", "password"}

	lower := strings.ToLower(name)
	for _, needle := range secretNames {
		if strings.Contains(lower, needle) {
			return true
		}
	}

	return false
}

// betterStackHostPort builds a host/port config.
func betterStackHostPort(host string, port int, timeout string) map[string]any {
	cfg := map[string]any{cfgKeyHost: host}
	if port > 0 {
		cfg[cfgKeyPort] = port
	}

	if timeout != "" {
		cfg[cfgKeyTimeout] = timeout
	}

	return cfg
}

// betterStackHost extracts a bare hostname from a monitor url, which for
// non-HTTP monitor types is already a hostname (optionally with a port).
func betterStackHost(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	if strings.Contains(trimmed, "://") {
		if parsed, err := url.Parse(trimmed); err == nil && parsed.Hostname() != "" {
			return parsed.Hostname()
		}
	}

	host, _ := splitHostPort(trimmed)

	return host
}

// betterStackPort normalizes the port attribute, which the API returns as a
// number, a string, or null.
func betterStackPort(raw any) int {
	switch v := raw.(type) {
	case float64:
		return int(v)
	case string:
		port, err := strconv.Atoi(v)
		if err != nil {
			return 0
		}

		return port
	default:
		return 0
	}
}

// convertBetterStackHeartbeat maps one heartbeat onto a SolidPing heartbeat
// check. Better Stack's `period` becomes the check period (the expected
// interval) and `grace` becomes the incident confirmation period.
func convertBetterStackHeartbeat(
	item *betterStackItem, slugs *slugSet, warn *warnings,
) (checks.ExportCheck, bool) {
	var heartbeat betterStackHeartbeat
	if err := json.Unmarshal(item.Attributes, &heartbeat); err != nil {
		warn.addf("heartbeat "+item.ID, "", "heartbeat skipped: its attributes could not be decoded")

		return checks.ExportCheck{}, false
	}

	name := heartbeat.Name
	if name == "" {
		name = "heartbeat-" + item.ID
	}

	check := checks.ExportCheck{
		Name:    name,
		Slug:    slugs.unique(name),
		Type:    string(checkerdef.CheckTypeHeartbeat),
		Enabled: !heartbeat.Paused,
		Period:  secondsToPeriod(heartbeat.Period),
		Config:  map[string]any{},
	}

	if heartbeat.Grace > 0 {
		check.ConfirmationPeriodSeconds = intPtr(heartbeat.Grace)
	}

	return check, true
}
