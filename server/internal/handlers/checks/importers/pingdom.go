package importers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// DefaultPingdomBaseURL is the public Pingdom API root.
const DefaultPingdomBaseURL = "https://api.pingdom.com"

const (
	// pingdomTimeout bounds the whole list + details fetch.
	pingdomTimeout = 90 * time.Second
	// pingdomConcurrency bounds the per-check detail calls in flight.
	pingdomConcurrency = 4
	// pingdomMaxBody bounds one API response.
	pingdomMaxBody = 8 << 20
)

// Pingdom check types, as they appear in the API.
const (
	pingdomHTTP       = "http"
	pingdomHTTPCustom = "httpcustom"
	pingdomDNS        = "dns"
	pingdomUDP        = "udp"
	pingdomSMTP       = "smtp"
	pingdomPOP3       = "pop3"
	pingdomIMAP       = "imap"
	pingdomPaused     = "paused"
	pingdomHTTPS      = "https"
	pingdomHTTPPort   = 80
	pingdomHTTPSPort  = 443
)

var (
	// ErrPingdomTokenRequired is returned when the body carries neither a token nor a payload.
	ErrPingdomTokenRequired = errors.New("a Pingdom API token or a pasted Pingdom JSON payload is required")
	// ErrPingdomUnauthorized is returned when Pingdom rejects the token.
	ErrPingdomUnauthorized = errors.New("the Pingdom API rejected the token")
	// ErrPingdomRateLimited is returned when Pingdom answers 429.
	ErrPingdomRateLimited = errors.New("the Pingdom API rate limit was reached, retry in a few minutes")
	// ErrPingdomAPI is returned for any other non-success API response.
	ErrPingdomAPI = errors.New("the Pingdom API returned an error")
	// ErrPingdomUnreachable is returned when the API could not be reached.
	ErrPingdomUnreachable = errors.New("the Pingdom API could not be reached")
)

// PingdomOptions configures the converter. The token is never an option: it
// arrives with the request body and lives for one Convert call.
type PingdomOptions struct {
	BaseURL string
	Client  *http.Client
	// TMSPageSize is the page size of the transaction check list; 0 means the
	// default. Exposed so tests can exercise pagination with small fixtures.
	TMSPageSize int
}

// PingdomConverter converts Pingdom checks, either fetched from the Pingdom API
// 3.1 with a read-only token, or pasted as JSON. The token is only sent as a
// Bearer header; it is never stored, logged, or echoed in an error.
type PingdomConverter struct {
	baseURL     string
	client      *http.Client
	tmsPageSize int
}

// NewPingdomConverter builds a converter with the supplied options.
func NewPingdomConverter(opts PingdomOptions) *PingdomConverter {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = DefaultPingdomBaseURL
	}

	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: pingdomTimeout}
	}

	pageSize := opts.TMSPageSize
	if pageSize <= 0 {
		pageSize = pingdomTMSPageSize
	}

	return &PingdomConverter{baseURL: base, client: client, tmsPageSize: pageSize}
}

// Source implements Converter.
func (c *PingdomConverter) Source() string { return SourcePingdom }

// pingdomEnvelope is the union of the accepted body shapes: a token request, a
// GET /checks response, or a single GET /checks/{id} response.
type pingdomEnvelope struct {
	Token  string            `json:"token"`
	Checks []json.RawMessage `json:"checks"`
	Check  json.RawMessage   `json:"check"`
}

// pingdomCheck is the subset of a Pingdom check we map. The list endpoint
// returns `type` as a string; the details endpoint returns an object keyed by
// the type name, so it is decoded lazily.
//
//nolint:tagliatelle // field names are Pingdom's, not ours
type pingdomCheck struct {
	ID                       int64             `json:"id"`
	Name                     string            `json:"name"`
	Hostname                 string            `json:"hostname"`
	Status                   string            `json:"status"`
	Resolution               int               `json:"resolution"`
	SendNotificationWhenDown int               `json:"sendnotificationwhendown"`
	Type                     json.RawMessage   `json:"type"`
	Tags                     []pingdomTag      `json:"tags"`
	ProbeFilters             []json.RawMessage `json:"probe_filters"`
}

type pingdomTag struct {
	Name string `json:"name"`
}

// pingdomSettings is the type-specific block of a check's details.
type pingdomSettings struct {
	URL              string            `json:"url"`
	Port             int               `json:"port"`
	Encryption       bool              `json:"encryption"`
	ShouldContain    string            `json:"shouldcontain"`
	ShouldNotContain string            `json:"shouldnotcontain"`
	PostData         string            `json:"postdata"`
	RequestHeaders   map[string]string `json:"requestheaders"`
	Username         string            `json:"username"`
	Password         string            `json:"password"`
	Auth             string            `json:"auth"`
	StringToSend     string            `json:"stringtosend"`
	StringToExpect   string            `json:"stringtoexpect"`
	ExpectedIP       string            `json:"expectedip"`
	Nameserver       string            `json:"nameserver"`
}

// Convert implements Converter using a background context.
func (c *PingdomConverter) Convert(input []byte) (*ConversionResult, error) {
	return c.ConvertContext(context.Background(), input)
}

// ConvertContext resolves the input (token fetch or pasted payload) into a list
// of raw checks, then converts them.
func (c *PingdomConverter) ConvertContext(ctx context.Context, input []byte) (*ConversionResult, error) {
	warn := &warnings{}

	items, err := c.collect(ctx, input, warn)
	if err != nil {
		return nil, err
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("%w: the Pingdom payload has no checks", ErrEmptyInput)
	}

	doc := newDocument(SourcePingdom)
	slugs := newSlugSet()

	for _, raw := range items {
		convert := convertPingdomCheck
		if isPingdomTMS(raw) {
			convert = convertPingdomTMS
		}

		if check, ok := convert(raw, slugs, warn); ok {
			doc.Checks = append(doc.Checks, check)
		}
	}

	if len(doc.Checks) == 0 {
		return nil, fmt.Errorf("%w: no Pingdom check could be mapped to a SolidPing check", ErrEmptyInput)
	}

	normalizeChecks(doc, warn)

	return &ConversionResult{Document: doc, Warnings: warn.result()}, nil
}

// collect returns one raw JSON object per check.
func (c *PingdomConverter) collect(
	ctx context.Context, input []byte, warn *warnings,
) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(input)

	// A bare array of check (or {"check": …}) objects.
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("parse pingdom payload: %w", err)
		}

		return unwrapPingdomItems(arr), nil
	}

	var env pingdomEnvelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return nil, fmt.Errorf("parse pingdom payload: %w", err)
	}

	if token := strings.TrimSpace(env.Token); token != "" {
		return c.fetch(ctx, token, warn)
	}

	switch {
	case len(env.Checks) > 0:
		return unwrapPingdomItems(env.Checks), nil
	case len(env.Check) > 0:
		return []json.RawMessage{env.Check}, nil
	case isPingdomTMS(trimmed):
		// A bare GET /tms/check/{id} response.
		return []json.RawMessage{json.RawMessage(trimmed)}, nil
	default:
		return nil, ErrPingdomTokenRequired
	}
}

// unwrapPingdomItems accepts both bare check objects and {"check": {...}} wrappers.
func unwrapPingdomItems(items []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(items))

	for _, item := range items {
		var wrapper struct {
			Check json.RawMessage `json:"check"`
		}

		if err := json.Unmarshal(item, &wrapper); err == nil && len(wrapper.Check) > 0 {
			out = append(out, wrapper.Check)

			continue
		}

		out = append(out, item)
	}

	return out
}

// fetch lists the checks, then reads each one's details with bounded concurrency.
func (c *PingdomConverter) fetch(ctx context.Context, token string, warn *warnings) ([]json.RawMessage, error) {
	body, err := c.get(ctx, token, "/api/3.1/checks?include_tags=true")
	if err != nil {
		return nil, err
	}

	var list struct {
		Checks []struct {
			ID int64 `json:"id"`
		} `json:"checks"`
	}

	if decodeErr := json.Unmarshal(body, &list); decodeErr != nil {
		return nil, fmt.Errorf("%w: unreadable check list", ErrPingdomAPI)
	}

	var raw struct {
		Checks []json.RawMessage `json:"checks"`
	}

	_ = json.Unmarshal(body, &raw)

	results := make([]json.RawMessage, len(list.Checks))
	errs := make([]error, len(list.Checks))

	var group sync.WaitGroup

	sem := make(chan struct{}, pingdomConcurrency)

	for i := range list.Checks {
		group.Add(1)

		sem <- struct{}{}

		go func(idx int, id int64) {
			defer group.Done()
			defer func() { <-sem }()

			detail, getErr := c.get(ctx, token, "/api/3.1/checks/"+strconv.FormatInt(id, 10))
			if getErr != nil {
				errs[idx] = getErr

				return
			}

			var wrapper struct {
				Check json.RawMessage `json:"check"`
			}

			if json.Unmarshal(detail, &wrapper) != nil || len(wrapper.Check) == 0 {
				errs[idx] = fmt.Errorf("%w: unreadable details for check %d", ErrPingdomAPI, id)

				return
			}

			results[idx] = wrapper.Check
		}(i, list.Checks[i].ID)
	}

	group.Wait()

	for _, e := range errs {
		if e != nil {
			return nil, e
		}
	}

	// Tags are only guaranteed on the list response: carry them over.
	merged := mergePingdomTags(results, raw.Checks)

	tms, err := c.fetchTMS(ctx, token, warn)
	if err != nil {
		return nil, err
	}

	return append(merged, tms...), nil
}

// mergePingdomTags copies the list item's tags into the details object when the
// details omit them.
func mergePingdomTags(details, list []json.RawMessage) []json.RawMessage {
	for i := range details {
		if i >= len(list) {
			break
		}

		var listItem pingdomCheck
		if json.Unmarshal(list[i], &listItem) != nil || len(listItem.Tags) == 0 {
			continue
		}

		var detail map[string]json.RawMessage
		if json.Unmarshal(details[i], &detail) != nil {
			continue
		}

		if _, has := detail["tags"]; has {
			continue
		}

		tags, err := json.Marshal(listItem.Tags)
		if err != nil {
			continue
		}

		detail["tags"] = tags

		if merged, err := json.Marshal(detail); err == nil {
			details[i] = merged
		}
	}

	return details
}

// get performs one authenticated GET against the fixed API base.
func (c *PingdomConverter) get(ctx context.Context, token, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, ErrPingdomUnreachable
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		// Not wrapped: transport errors can embed the request URL.
		return nil, ErrPingdomUnreachable
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, pingdomMaxBody))
	if err != nil {
		return nil, fmt.Errorf("%w: could not read the response body", ErrPingdomUnreachable)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w (HTTP %d)", ErrPingdomUnauthorized, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, ErrPingdomRateLimited
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: %w (HTTP 404)", ErrPingdomAPI, errPingdomNotFound)
	case resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices:
		// The body is intentionally not included: it can echo the request.
		return nil, fmt.Errorf("%w (HTTP %d)", ErrPingdomAPI, resp.StatusCode)
	}

	return body, nil
}

// pingdomTypeAndSettings extracts the type name and its settings from either
// the list form (`"type": "http"`) or the details form (`"type": {"http": {…}}`).
func pingdomTypeAndSettings(raw json.RawMessage) (string, pingdomSettings) {
	var settings pingdomSettings

	var name string
	if json.Unmarshal(raw, &name) == nil {
		return strings.ToLower(name), settings
	}

	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return "", settings
	}

	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	if len(keys) == 0 {
		return "", settings
	}

	_ = json.Unmarshal(obj[keys[0]], &settings)

	return strings.ToLower(keys[0]), settings
}

// convertPingdomCheck maps one Pingdom check.
//
//nolint:funlen // one dispatch per Pingdom type
func convertPingdomCheck(raw json.RawMessage, slugs *slugSet, warn *warnings) (checks.ExportCheck, bool) {
	var source pingdomCheck
	if err := json.Unmarshal(raw, &source); err != nil {
		warn.addf("check", "", "check skipped: it could not be decoded")

		return checks.ExportCheck{}, false
	}

	name := source.Name
	if name == "" {
		name = source.Hostname
	}

	typeName, settings := pingdomTypeAndSettings(source.Type)

	check := checks.ExportCheck{
		Name:    name,
		Slug:    slugs.unique(name),
		Enabled: source.Status != pingdomPaused,
		Period:  secondsToPeriod(source.Resolution * 60),
		Config:  map[string]any{},
		Labels:  pingdomLabels(source.Tags),
	}

	host := source.Hostname

	switch typeName {
	case pingdomHTTP:
		check.Type = string(checkerdef.CheckTypeHTTP)
		check.Config = pingdomHTTPConfig(host, &settings, name, warn)
	case pingdomHTTPCustom:
		check.Type = string(checkerdef.CheckTypeHTTP)
		check.Config = pingdomHTTPConfig(host, &settings, name, warn)

		warn.addf(name, "type",
			"custom HTTP checks (XML response values) are not supported — imported as a plain HTTP check on the URL")
	case srcTCP:
		check.Type = string(checkerdef.CheckTypeTCP)
		check.Config = pingdomExchangeConfig(host, &settings)
	case srcPing:
		check.Type = string(checkerdef.CheckTypeICMP)
		check.Config = map[string]any{cfgKeyHost: host}
	case pingdomDNS:
		check.Type = string(checkerdef.CheckTypeDNS)
		check.Config = pingdomDNSConfig(host, &settings)
	case pingdomUDP:
		check.Type = string(checkerdef.CheckTypeUDP)
		check.Config = pingdomExchangeConfig(host, &settings)
	case pingdomSMTP:
		check.Type = string(checkerdef.CheckTypeSMTP)
		check.Config = pingdomMailConfig(host, true, &settings, name, warn)
	case pingdomPOP3:
		check.Type = string(checkerdef.CheckTypePOP3)
		check.Config = pingdomMailConfig(host, false, &settings, name, warn)
	case pingdomIMAP:
		check.Type = string(checkerdef.CheckTypeIMAP)
		check.Config = pingdomMailConfig(host, false, &settings, name, warn)
	default:
		warn.addf(name, "type", "check skipped: Pingdom type %q has no SolidPing counterpart "+
			"(legacy transaction checks are not imported; transaction checks are read from the TMS API)", typeName)

		return checks.ExportCheck{}, false
	}

	pingdomApplyAlerting(&source, &check, name, warn)

	return check, true
}

// pingdomApplyAlerting maps the "alert after n failures" setting and flags the
// probe filters, which have no counterpart.
func pingdomApplyAlerting(source *pingdomCheck, check *checks.ExportCheck, name string, warn *warnings) {
	if source.SendNotificationWhenDown > 0 && source.Resolution > 0 {
		check.ConfirmationPeriodSeconds = intPtr(source.SendNotificationWhenDown * source.Resolution * 60)
	}

	if len(source.ProbeFilters) > 0 {
		warn.addf(name, "probe_filters",
			"probe filters are ignored — pick SolidPing regions on the check instead")
	}
}

// pingdomLabels turns tags into labels; tag names are slugified into valid keys.
func pingdomLabels(tags []pingdomTag) map[string]string {
	if len(tags) == 0 {
		return nil
	}

	labels := map[string]string{}

	for _, tag := range tags {
		if strings.TrimSpace(tag.Name) == "" {
			continue
		}

		labels[slugify(tag.Name)] = "true"
	}

	if len(labels) == 0 {
		return nil
	}

	return labels
}

// pingdomHTTPConfig builds the http checker config.
func pingdomHTTPConfig(host string, settings *pingdomSettings, name string, warn *warnings) map[string]any {
	scheme := srcHTTP
	defaultPort := pingdomHTTPPort

	if settings.Encryption {
		scheme = pingdomHTTPS
		defaultPort = pingdomHTTPSPort
	}

	authority := host
	if settings.Port > 0 && settings.Port != defaultPort {
		authority = host + ":" + strconv.Itoa(settings.Port)
	}

	path := settings.URL
	if path == "" {
		path = "/"
	} else if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	u := url.URL{Scheme: scheme, Host: authority}
	cfg := map[string]any{cfgKeyURL: u.String() + path}

	if settings.PostData != "" {
		cfg["method"] = "POST"
		cfg["body"] = settings.PostData
	}

	if settings.ShouldContain != "" {
		cfg["body_expect"] = settings.ShouldContain
	}

	if settings.ShouldNotContain != "" {
		cfg["body_reject"] = settings.ShouldNotContain
	}

	pingdomApplyHeaders(cfg, settings.RequestHeaders, name, warn)

	if settings.Username != "" || settings.Password != "" || settings.Auth != "" {
		warn.addf(name, "auth",
			"basic-auth credentials were deliberately not imported (SolidPing never re-persists secrets "+
				"from a foreign export) — re-enter them on the check")
	}

	return cfg
}

// pingdomApplyHeaders splits request headers into public and encrypted ones.
func pingdomApplyHeaders(cfg map[string]any, requestHeaders map[string]string, name string, warn *warnings) {
	headers := map[string]any{}
	secretHeaders := map[string]any{}

	for key, value := range requestHeaders {
		if key == "" {
			continue
		}

		if betterStackIsSecretHeader(key) {
			secretHeaders[key] = value

			continue
		}

		headers[key] = value
	}

	if len(headers) > 0 {
		cfg["headers"] = headers
	}

	if len(secretHeaders) > 0 {
		cfg["secretHeaders"] = secretHeaders

		warn.addf(name, "requestheaders",
			"%d credential-bearing header(s) were imported into the encrypted secretHeaders field",
			len(secretHeaders))
	}
}

// pingdomExchangeConfig builds a tcp/udp config with the optional send/expect strings.
func pingdomExchangeConfig(host string, settings *pingdomSettings) map[string]any {
	cfg := map[string]any{cfgKeyHost: host}
	if settings.Port > 0 {
		cfg[cfgKeyPort] = settings.Port
	}

	if settings.StringToSend != "" {
		cfg["send_data"] = settings.StringToSend
	}

	if settings.StringToExpect != "" {
		cfg["expect_data"] = settings.StringToExpect
	}

	return cfg
}

// pingdomDNSConfig builds the dns config.
func pingdomDNSConfig(host string, settings *pingdomSettings) map[string]any {
	cfg := map[string]any{cfgKeyHost: host, "record_type": "A"}
	if settings.Nameserver != "" {
		cfg["nameserver"] = withDefaultPort(settings.Nameserver, dnsDefaultPort)
	}

	if settings.ExpectedIP != "" {
		cfg["expected_ips"] = []any{settings.ExpectedIP}
	}

	return cfg
}

const smtpImplicitTLSPort = 465

// pingdomMailConfig builds the smtp/pop3/imap config.
func pingdomMailConfig(
	host string, isSMTP bool, settings *pingdomSettings, name string, warn *warnings,
) map[string]any {
	cfg := map[string]any{cfgKeyHost: host}
	if settings.Port > 0 {
		cfg[cfgKeyPort] = settings.Port
	}

	if settings.StringToExpect != "" {
		cfg["expect_greeting"] = settings.StringToExpect
	}

	if settings.Encryption {
		if isSMTP {
			// The SMTP checker has no tls field: port 465 is implicit TLS by itself,
			// any other port needs starttls.
			if settings.Port != smtpImplicitTLSPort {
				cfg["starttls"] = true
			}
		} else {
			cfg["tls"] = true
		}
	}

	if settings.Username != "" || settings.Password != "" || settings.Auth != "" {
		warn.addf(name, "auth",
			"credentials were deliberately not imported (SolidPing never re-persists secrets "+
				"from a foreign export) — re-enter them on the check")
	}

	return cfg
}
