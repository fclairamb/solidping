package importers

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// GatusConverter turns a Gatus config.yaml into an ExportDocument.
//
// Gatus has no config-export API (its runtime API returns statuses, not the
// endpoint definitions), so the YAML file is the source of truth and the input
// to this converter.
type GatusConverter struct{}

// Source implements Converter.
func (c *GatusConverter) Source() string { return SourceGatus }

// gatusConfig is the subset of a Gatus config file we read.
//
//nolint:tagliatelle // field names are Gatus's kebab-case, not ours
type gatusConfig struct {
	Endpoints         []gatusEndpoint `yaml:"endpoints"`
	ExternalEndpoints []struct {
		Name  string `yaml:"name"`
		Group string `yaml:"group"`
	} `yaml:"external-endpoints"`
}

// gatusEndpoint mirrors one entry of the Gatus `endpoints:` list.
//
//nolint:tagliatelle // field names are Gatus's kebab-case, not ours
type gatusEndpoint struct {
	Enabled    *bool             `yaml:"enabled"`
	Name       string            `yaml:"name"`
	Group      string            `yaml:"group"`
	URL        string            `yaml:"url"`
	Method     string            `yaml:"method"`
	Body       string            `yaml:"body"`
	Headers    map[string]string `yaml:"headers"`
	Interval   string            `yaml:"interval"`
	Conditions []string          `yaml:"conditions"`
	Alerts     []struct {
		Type string `yaml:"type"`
	} `yaml:"alerts"`
	DNS *struct {
		QueryName string `yaml:"query-name"`
		QueryType string `yaml:"query-type"`
	} `yaml:"dns"`
	SSH *struct {
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"ssh"`
	Client *struct {
		Insecure       *bool  `yaml:"insecure"`
		IgnoreRedirect *bool  `yaml:"ignore-redirect"`
		Timeout        string `yaml:"timeout"`
		DNSResolver    string `yaml:"dns-resolver"`
	} `yaml:"client"`
}

// gatusConditionRe splits a Gatus condition into its left operand, operator and
// expected value. Values may contain spaces (`any(1, 2)`), so the value is
// greedy to end of line.
var gatusConditionRe = regexp.MustCompile(`^\s*(\S+)\s*(==|!=|>=|<=|>|<)\s*(.+?)\s*$`)

// gatusAnyRe matches Gatus's any(a, b, c) value form.
var gatusAnyRe = regexp.MustCompile(`^any\((.*)\)$`)

// gatusPatRe matches Gatus's pat(glob) value form.
var gatusPatRe = regexp.MustCompile(`^pat\((.*)\)$`)

// gatusHasRe matches Gatus's has([BODY].path) left-operand form.
var gatusHasRe = regexp.MustCompile(`^has\(\[BODY\](.*)\)$`)

// gatusLenRe matches Gatus's len([BODY].path) left-operand form.
var gatusLenRe = regexp.MustCompile(`^len\(\[BODY\](.*)\)$`)

const (
	gatusPlaceholderStatus  = "[STATUS]"
	gatusPlaceholderBody    = "[BODY]"
	gatusPlaceholderCert    = "[CERTIFICATE_EXPIRATION]"
	gatusPlaceholderDomain  = "[DOMAIN_EXPIRATION]"
	gatusPlaceholderConn    = "[CONNECTED]"
	gatusPlaceholderRcode   = "[DNS_RCODE]"
	gatusDefaultHTTPMethod  = "GET"
	gatusDefaultIntervalStr = "60s"
	hoursPerDay             = 24
	// maxExtraChecksPerEndpoint is the number of extra checks a single endpoint
	// can spawn (an ssl check and a domain check).
	maxExtraChecksPerEndpoint = 2
)

// Convert implements Converter. Parse failures on the document as a whole are
// hard errors; anything an individual endpoint can't express becomes a warning
// and the endpoint is still imported (or skipped, when nothing maps at all).
func (c *GatusConverter) Convert(input []byte) (*ConversionResult, error) {
	var cfg gatusConfig
	if err := yaml.Unmarshal(input, &cfg); err != nil {
		return nil, fmt.Errorf("parse gatus yaml: %w", err)
	}

	if len(cfg.Endpoints) == 0 {
		return nil, fmt.Errorf("%w: the config has no `endpoints:` list", ErrEmptyInput)
	}

	warn := &warnings{}
	doc := newDocument(SourceGatus)
	slugs := newSlugSet()

	if len(cfg.ExternalEndpoints) > 0 {
		warn.addDocf("%d external-endpoint(s) were not imported: Gatus push endpoints have no "+
			"direct equivalent — recreate them as SolidPing heartbeat checks",
			len(cfg.ExternalEndpoints))
	}

	for idx := range cfg.Endpoints {
		converted := c.convertEndpoint(&cfg.Endpoints[idx], slugs, warn)
		doc.Checks = append(doc.Checks, converted...)
	}

	if len(doc.Checks) == 0 {
		return nil, fmt.Errorf("%w: no gatus endpoint could be mapped to a SolidPing check", ErrEmptyInput)
	}

	normalizeChecks(doc, warn)

	return &ConversionResult{Document: doc, Warnings: warn.result()}, nil
}

// convertEndpoint maps one Gatus endpoint to zero or more checks. A single
// endpoint can yield extra checks when it carries certificate/domain expiration
// conditions, which SolidPing models as dedicated ssl/domain checks.
//
//nolint:cyclop,funlen,exhaustive // one linear pass; the default arm rejects unmapped types
func (c *GatusConverter) convertEndpoint(
	endpoint *gatusEndpoint, slugs *slugSet, warn *warnings,
) []checks.ExportCheck {
	name := endpoint.Name
	if name == "" {
		name = endpoint.URL
	}

	checkType, target, err := gatusCheckType(endpoint)
	if err != nil {
		warn.addf(name, "url", "endpoint skipped: %s", err.Error())

		return nil
	}

	base := checks.ExportCheck{
		Name:    name,
		Slug:    slugs.unique(name),
		Type:    string(checkType),
		Group:   endpoint.Group,
		Enabled: endpoint.Enabled == nil || *endpoint.Enabled,
		Period:  gatusInterval(endpoint, name, warn),
		Config:  map[string]any{},
	}

	timeout := gatusClientTimeout(endpoint, name, warn)

	switch checkType {
	case checkerdef.CheckTypeHTTP:
		base.Config = gatusHTTPConfig(endpoint, timeout)
		gatusApplyHTTPClientOptions(endpoint, base.Config)
	case checkerdef.CheckTypeDNS:
		base.Config = gatusDNSConfig(endpoint, target, timeout)
	case checkerdef.CheckTypeTCP:
		base.Config = map[string]any{cfgKeyHost: target.host, cfgKeyPort: target.port}
		if target.tls {
			base.Config["tls"] = true
		}
	case checkerdef.CheckTypeUDP:
		base.Config = map[string]any{cfgKeyHost: target.host, cfgKeyPort: target.port}
	case checkerdef.CheckTypeICMP:
		base.Config = map[string]any{cfgKeyHost: target.host}
	case checkerdef.CheckTypeSSH:
		base.Config = gatusSSHConfig(endpoint, target, name, warn)
	case checkerdef.CheckTypeWebSocket:
		base.Config = map[string]any{cfgKeyURL: endpoint.URL}
	default:
		warn.addf(name, "url", "endpoint skipped: unsupported target type %q", checkType)

		return nil
	}

	if timeout != "" {
		if _, taken := base.Config[cfgKeyTimeout]; !taken {
			base.Config[cfgKeyTimeout] = timeout
		}
	}

	out := make([]checks.ExportCheck, 0, 1+maxExtraChecksPerEndpoint)
	out = append(out, base)

	extra := c.applyConditions(endpoint, &out[0], checkType, target, slugs, warn)
	out = append(out, extra...)

	if len(endpoint.Alerts) > 0 {
		warn.addf(name, "alerts",
			"alerting/notification bindings are not imported — configure SolidPing integrations instead")
	}

	if endpoint.Client != nil && endpoint.Client.Insecure != nil && *endpoint.Client.Insecure &&
		checkType != checkerdef.CheckTypeHTTP {
		warn.addf(name, "client.insecure",
			"skipping TLS verification is not supported by the SolidPing %s checker", checkType)
	}

	if endpoint.Client != nil && endpoint.Client.IgnoreRedirect != nil && *endpoint.Client.IgnoreRedirect &&
		checkType != checkerdef.CheckTypeHTTP {
		warn.addf(name, "client.ignore-redirect",
			"redirect handling is not configurable on the SolidPing %s checker", checkType)
	}

	if endpoint.Client != nil && endpoint.Client.DNSResolver != "" && checkType != checkerdef.CheckTypeDNS {
		warn.addf(name, "client.dns-resolver",
			"a custom DNS resolver is only supported on SolidPing dns checks")
	}

	return out
}

// gatusTarget is the parsed form of an endpoint URL.
type gatusTarget struct {
	host string
	port int
	tls  bool
}

// gatusCheckType decides the SolidPing check type for an endpoint and parses
// its target. A `dns:` block always wins: for DNS endpoints Gatus uses `url` as
// the resolver address, not as the monitored target.
func gatusCheckType(endpoint *gatusEndpoint) (checkerdef.CheckType, gatusTarget, error) {
	if endpoint.DNS != nil {
		return checkerdef.CheckTypeDNS, gatusTarget{host: endpoint.URL}, nil
	}

	if endpoint.URL == "" {
		return "", gatusTarget{}, errNoURL
	}

	parsed, err := url.Parse(endpoint.URL)
	if err != nil {
		return "", gatusTarget{}, fmt.Errorf("cannot parse url %q: %w", endpoint.URL, err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case srcHTTP, "https":
		return checkerdef.CheckTypeHTTP, gatusTarget{host: parsed.Hostname()}, nil
	case "ws", "wss", "websocket":
		return checkerdef.CheckTypeWebSocket, gatusTarget{host: parsed.Hostname()}, nil
	case srcTCP, "tls", "starttls":
		target := gatusHostPort(parsed)
		target.tls = scheme != srcTCP

		return checkerdef.CheckTypeTCP, target, nil
	case "udp":
		return checkerdef.CheckTypeUDP, gatusHostPort(parsed), nil
	case "icmp", srcPing:
		return checkerdef.CheckTypeICMP, gatusTarget{host: parsed.Hostname()}, nil
	case "ssh":
		return checkerdef.CheckTypeSSH, gatusHostPort(parsed), nil
	case "sctp":
		return "", gatusTarget{}, errUnsupportedScheme("sctp")
	case "":
		return "", gatusTarget{}, fmt.Errorf("%w: %q", errNoScheme, endpoint.URL)
	default:
		return "", gatusTarget{}, errUnsupportedScheme(scheme)
	}
}

var (
	// errNoURL reports an endpoint with neither a url nor a dns block.
	errNoURL = errors.New("endpoint has no url and no dns block")
	// errNoScheme reports a url we cannot classify because it has no scheme.
	errNoScheme = errors.New("url has no scheme")
	// errUnmappableScheme reports a url scheme with no SolidPing check type.
	errUnmappableScheme = errors.New("url scheme has no SolidPing check type")
)

// errUnsupportedScheme builds the "no SolidPing counterpart" error for a scheme.
func errUnsupportedScheme(scheme string) error {
	return fmt.Errorf("%w: %q", errUnmappableScheme, scheme)
}

// gatusHostPort splits host:port out of a parsed URL.
func gatusHostPort(parsed *url.URL) gatusTarget {
	target := gatusTarget{host: parsed.Hostname()}
	if portStr := parsed.Port(); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			target.port = port
		}
	}

	// Bare "tcp://host:port" without a scheme separator still parses; guard the
	// degenerate "no host" case by falling back to the opaque part.
	if target.host == "" && parsed.Opaque != "" {
		if host, portStr, err := net.SplitHostPort(parsed.Opaque); err == nil {
			target.host = host
			if port, convErr := strconv.Atoi(portStr); convErr == nil {
				target.port = port
			}
		}
	}

	return target
}

// gatusInterval maps the endpoint interval onto the check period.
func gatusInterval(endpoint *gatusEndpoint, name string, warn *warnings) string {
	raw := endpoint.Interval
	if raw == "" {
		raw = gatusDefaultIntervalStr
	}

	duration, err := time.ParseDuration(raw)
	if err != nil {
		warn.addf(name, "interval", "unparseable interval %q, falling back to 1m", raw)

		return "1m"
	}

	return durationToPeriod(duration)
}

// gatusClientTimeout maps client.timeout onto the checker timeout string.
func gatusClientTimeout(endpoint *gatusEndpoint, name string, warn *warnings) string {
	if endpoint.Client == nil || endpoint.Client.Timeout == "" {
		return ""
	}

	duration, err := time.ParseDuration(endpoint.Client.Timeout)
	if err != nil {
		warn.addf(name, "client.timeout", "unparseable timeout %q, using the checker default", endpoint.Client.Timeout)

		return ""
	}

	return durationToPeriod(duration)
}

// gatusHTTPConfig builds the http checker config from the endpoint request bits.
func gatusHTTPConfig(endpoint *gatusEndpoint, timeout string) map[string]any {
	cfg := map[string]any{cfgKeyURL: endpoint.URL}

	method := endpoint.Method
	if method == "" {
		method = gatusDefaultHTTPMethod
	}

	cfg["method"] = strings.ToUpper(method)

	if endpoint.Body != "" {
		cfg["body"] = endpoint.Body
	}

	if len(endpoint.Headers) > 0 {
		headers := make(map[string]any, len(endpoint.Headers))
		for k, v := range endpoint.Headers {
			headers[k] = v
		}

		cfg["headers"] = headers
	}

	if timeout != "" {
		cfg[cfgKeyTimeout] = timeout
	}

	return cfg
}

// gatusApplyHTTPClientOptions maps Gatus's client.insecure / client.ignore-redirect
// onto the http checker's verifySsl / followRedirects, mutating cfg in place.
func gatusApplyHTTPClientOptions(endpoint *gatusEndpoint, cfg map[string]any) {
	if endpoint.Client == nil {
		return
	}

	if endpoint.Client.Insecure != nil && *endpoint.Client.Insecure {
		cfg["verifySsl"] = false
	}

	if endpoint.Client.IgnoreRedirect != nil && *endpoint.Client.IgnoreRedirect {
		cfg["followRedirects"] = false
	}
}

// gatusDNSConfig builds the dns checker config. Gatus puts the monitored name
// in dns.query-name and the resolver in url.
func gatusDNSConfig(endpoint *gatusEndpoint, target gatusTarget, timeout string) map[string]any {
	cfg := map[string]any{cfgKeyHost: endpoint.DNS.QueryName}

	if endpoint.DNS.QueryType != "" {
		cfg["record_type"] = strings.ToUpper(endpoint.DNS.QueryType)
	}

	if resolver := withDefaultPort(strings.TrimPrefix(target.host, "dns://"), dnsDefaultPort); resolver != "" {
		cfg["nameserver"] = resolver
	}

	if timeout != "" {
		cfg[cfgKeyTimeout] = timeout
	}

	return cfg
}

// gatusSSHConfig builds the ssh checker config. Gatus stores the credential in
// a dedicated ssh block; the password is deliberately NOT imported.
func gatusSSHConfig(endpoint *gatusEndpoint, target gatusTarget, name string, warn *warnings) map[string]any {
	cfg := map[string]any{cfgKeyHost: target.host}
	if target.port != 0 {
		cfg[cfgKeyPort] = target.port
	}

	if endpoint.SSH != nil && (endpoint.SSH.Username != "" || endpoint.SSH.Password != "") {
		// SolidPing's ssh checker does have username/password/private_key
		// fields — the credential is dropped as a deliberate security policy,
		// not for lack of somewhere to put it: a foreign export can carry
		// plaintext secrets, and silently re-persisting them is not something
		// an import should do behind the operator's back. A username with no
		// credential also fails checker validation, so neither half is kept.
		warn.addf(name, "ssh",
			"the SSH credentials were deliberately not imported (SolidPing never re-persists secrets "+
				"from a foreign export) — re-enter the username and password/key on the check")
	}

	return cfg
}

// applyConditions folds the endpoint conditions into the check config and
// returns any additional checks they imply (ssl / domain expiration).
//
//nolint:cyclop // one dispatch per Gatus placeholder
func (c *GatusConverter) applyConditions(
	endpoint *gatusEndpoint, check *checks.ExportCheck, checkType checkerdef.CheckType,
	target gatusTarget, slugs *slugSet, warn *warnings,
) []checks.ExportCheck {
	var (
		extra      []checks.ExportCheck
		assertions []checkhttp.AssertionNode
	)

	name := check.Name

	for _, raw := range endpoint.Conditions {
		cond, ok := parseGatusCondition(raw)
		if !ok {
			warn.addf(name, "conditions", "condition %q could not be parsed and was dropped", raw)

			continue
		}

		switch {
		case cond.left == gatusPlaceholderConn:
			// Connectivity is implicit in every SolidPing check.
		case cond.left == gatusPlaceholderRcode:
			if checkType != checkerdef.CheckTypeDNS {
				warn.addf(name, "conditions", "condition %q only applies to DNS endpoints and was dropped", raw)
			}
		case cond.left == gatusPlaceholderStatus:
			applyGatusStatusCondition(cond, check, raw, warn)
		case cond.left == gatusPlaceholderCert:
			if extraCheck, made := gatusExpiryCheck(
				cond, check, checkerdef.CheckTypeSSL, target, slugs, raw, warn); made {
				extra = append(extra, extraCheck)
			}
		case cond.left == gatusPlaceholderDomain:
			if extraCheck, made := gatusExpiryCheck(
				cond, check, checkerdef.CheckTypeDomain, target, slugs, raw, warn); made {
				extra = append(extra, extraCheck)
			}
		case gatusLenRe.MatchString(cond.left):
			warn.addf(name, "conditions", "condition %q uses len(), which has no SolidPing equivalent", raw)
		case gatusHasRe.MatchString(cond.left):
			if node, made := gatusHasAssertion(cond); made {
				assertions = append(assertions, node)
			} else {
				warn.addf(name, "conditions", "condition %q could not be mapped", raw)
			}
		case cond.left == gatusPlaceholderBody:
			applyGatusBodyCondition(cond, check, checkType, raw, warn)
		case strings.HasPrefix(cond.left, gatusPlaceholderBody):
			if checkType != checkerdef.CheckTypeHTTP {
				warn.addf(name, "conditions", "condition %q only applies to HTTP endpoints and was dropped", raw)

				continue
			}

			if node, made := gatusBodyPathAssertion(cond); made {
				assertions = append(assertions, node)
			} else {
				warn.addf(name, "conditions", "condition %q uses an operator with no SolidPing equivalent", raw)
			}
		default:
			warn.addf(name, "conditions", "condition %q has no SolidPing equivalent and was dropped", raw)
		}
	}

	if len(assertions) > 0 {
		root := gatusAssertionRoot(assertions)
		check.Config["json_path_assertions"] = assertionConfig(&root)
	}

	return extra
}

// gatusCondition is a parsed Gatus condition.
type gatusCondition struct {
	left     string
	operator string
	value    string
}

// parseGatusCondition splits "<left> <op> <value>" into its parts.
func parseGatusCondition(raw string) (gatusCondition, bool) {
	m := gatusConditionRe.FindStringSubmatch(raw)
	if m == nil {
		return gatusCondition{}, false
	}

	return gatusCondition{left: m[1], operator: m[2], value: m[3]}, true
}

// applyGatusStatusCondition maps a [STATUS] condition onto the http config.
func applyGatusStatusCondition(cond gatusCondition, check *checks.ExportCheck, raw string, warn *warnings) {
	if cond.operator != "==" {
		warn.addf(check.Name, "conditions",
			"condition %q uses a range operator; SolidPing expects explicit codes or NXX wildcards", raw)

		return
	}

	if m := gatusAnyRe.FindStringSubmatch(cond.value); m != nil {
		parts := strings.Split(m[1], ",")
		codes := make([]any, 0, len(parts))

		for _, p := range parts {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				codes = append(codes, trimmed)
			}
		}

		check.Config["expected_status_codes"] = codes

		return
	}

	if code, err := strconv.Atoi(cond.value); err == nil {
		check.Config["expected_status"] = code

		return
	}

	// Wildcards like 2XX pass through as a single expected code pattern.
	check.Config["expected_status_codes"] = []any{cond.value}
}

// applyGatusBodyCondition maps a whole-body [BODY] condition.
func applyGatusBodyCondition(
	cond gatusCondition, check *checks.ExportCheck, checkType checkerdef.CheckType, raw string, warn *warnings,
) {
	if checkType == checkerdef.CheckTypeDNS {
		// For DNS endpoints Gatus compares [BODY] against the resolved records.
		values, _ := check.Config["expected_values"].([]any)
		check.Config["expected_values"] = append(values, cond.value)

		return
	}

	if checkType != checkerdef.CheckTypeHTTP {
		warn.addf(check.Name, "conditions", "condition %q only applies to HTTP endpoints and was dropped", raw)

		return
	}

	if m := gatusPatRe.FindStringSubmatch(cond.value); m != nil {
		pattern := gatusGlobToRegex(m[1])
		if cond.operator == "!=" {
			check.Config["body_pattern_reject"] = pattern
		} else {
			check.Config["body_pattern"] = pattern
		}

		return
	}

	if cond.operator == "!=" {
		check.Config["body_reject"] = cond.value

		return
	}

	check.Config["body_expect"] = cond.value
}

// gatusHasAssertion turns `has([BODY].path) == true|false` into an
// exists / not_exists JSONPath assertion.
func gatusHasAssertion(cond gatusCondition) (checkhttp.AssertionNode, bool) {
	match := gatusHasRe.FindStringSubmatch(cond.left)
	if match == nil {
		return checkhttp.AssertionNode{}, false
	}

	operator := "exists"
	if strings.EqualFold(cond.value, "false") {
		operator = "not_exists"
	}

	return checkhttp.AssertionNode{
		Type:     checkhttp.NodeTypeAssertion,
		Path:     gatusJSONPath(match[1]),
		Operator: operator,
	}, true
}

// gatusBodyPathAssertion turns `[BODY].path <op> value` into a JSONPath assertion.
func gatusBodyPathAssertion(cond gatusCondition) (checkhttp.AssertionNode, bool) {
	operator, ok := gatusOperator(cond.operator)
	if !ok {
		return checkhttp.AssertionNode{}, false
	}

	value := cond.value
	if m := gatusPatRe.FindStringSubmatch(value); m != nil {
		operator = "regex"
		value = gatusGlobToRegex(m[1])
	}

	return checkhttp.AssertionNode{
		Type:     checkhttp.NodeTypeAssertion,
		Path:     gatusJSONPath(strings.TrimPrefix(cond.left, gatusPlaceholderBody)),
		Operator: operator,
		Value:    value,
	}, true
}

// gatusAssertionRoot wraps multiple assertions in an AND node; a single
// assertion is emitted bare.
func gatusAssertionRoot(nodes []checkhttp.AssertionNode) checkhttp.AssertionNode {
	if len(nodes) == 1 {
		return nodes[0]
	}

	return checkhttp.AssertionNode{Type: checkhttp.NodeTypeAnd, Children: nodes}
}

// gatusOperator maps a Gatus comparison operator to a SolidPing assertion operator.
func gatusOperator(op string) (string, bool) {
	switch op {
	case "==":
		return "eq", true
	case "!=":
		return "neq", true
	case ">":
		return "gt", true
	case ">=":
		return "gte", true
	case "<":
		return "lt", true
	case "<=":
		return "lte", true
	default:
		return "", false
	}
}

// gatusJSONPath turns a Gatus body path suffix (".data.name", "[0].id") into a
// JSONPath expression rooted at $.
func gatusJSONPath(suffix string) string {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return "$"
	}

	if strings.HasPrefix(suffix, ".") || strings.HasPrefix(suffix, "[") {
		return "$" + suffix
	}

	return "$." + suffix
}

// gatusGlobToRegex converts a Gatus pat() glob into an anchored regex.
func gatusGlobToRegex(glob string) string {
	var builder strings.Builder

	builder.WriteString("^")

	for i, part := range strings.Split(glob, "*") {
		if i > 0 {
			builder.WriteString(".*")
		}

		builder.WriteString(regexp.QuoteMeta(part))
	}

	builder.WriteString("$")

	return builder.String()
}

// gatusExpiryCheck builds the extra ssl / domain check implied by a
// [CERTIFICATE_EXPIRATION] / [DOMAIN_EXPIRATION] condition.
func gatusExpiryCheck(
	cond gatusCondition, parent *checks.ExportCheck, checkType checkerdef.CheckType,
	target gatusTarget, slugs *slugSet, raw string, warn *warnings,
) (checks.ExportCheck, bool) {
	if target.host == "" {
		warn.addf(parent.Name, "conditions", "condition %q was dropped: the endpoint has no resolvable host", raw)

		return checks.ExportCheck{}, false
	}

	days := gatusExpiryDays(cond.value)
	if days <= 0 {
		warn.addf(parent.Name, "conditions", "condition %q has an unparseable threshold and was dropped", raw)

		return checks.ExportCheck{}, false
	}

	suffix := "-ssl"
	cfg := map[string]any{cfgKeyHost: target.host, "criticalDays": days, "warningDays": days}

	if checkType == checkerdef.CheckTypeDomain {
		suffix = "-domain"
		cfg = map[string]any{"domain": target.host, "thresholdDays": days}
	}

	warn.addf(parent.Name, "conditions",
		"condition %q was split out into a dedicated %s check", raw, checkType)

	return checks.ExportCheck{
		Name:    parent.Name + suffix,
		Slug:    slugs.unique(parent.Name + suffix),
		Type:    string(checkType),
		Group:   parent.Group,
		Enabled: parent.Enabled,
		Period:  parent.Period,
		Config:  cfg,
	}, true
}

// gatusExpiryDays converts a Gatus expiration threshold ("48h", "720h") to days.
func gatusExpiryDays(value string) int {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0
	}

	days := int(duration.Hours() / hoursPerDay)
	if days == 0 && duration > 0 {
		days = 1
	}

	return days
}
