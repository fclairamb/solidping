package checkhttp

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// configKeySecretHeaders is the config map key for secret headers.
	configKeySecretHeaders = "secretHeaders"

	// configKeyBasicAuth is the reserved config map key holding a basic-auth
	// credential as a single "user:pass" string. It is a secret field, so both
	// halves of the credential are encrypted at rest.
	configKeyBasicAuth = "basicAuth"

	// configKeyUsername / configKeyPassword are the legacy top-level basic-auth
	// keys. They are still accepted on input (and honored at execution time
	// forever, for rows that predate the fold), but NormalizeConfig folds them
	// into configKeyBasicAuth on every write.
	configKeyUsername = "username"
	configKeyPassword = "password"

	// basicAuthSeparator separates the username from the password in a
	// configKeyBasicAuth value (RFC 7617).
	basicAuthSeparator = ":"
)

// MatchStatusCode checks if the actual status code matches any of the given patterns.
// Patterns can be exact codes like "200" or wildcards like "2XX" (matches 200-299).
func MatchStatusCode(actual int, patterns []string) bool {
	for _, pattern := range patterns {
		if matchSinglePattern(actual, pattern) {
			return true
		}
	}
	return false
}

// matchSinglePattern checks if the actual status code matches a single pattern.
func matchSinglePattern(actual int, pattern string) bool {
	pattern = strings.ToUpper(pattern)
	if strings.HasSuffix(pattern, "XX") && len(pattern) == 3 {
		// Wildcard match: "2XX" matches 200-299
		prefix := pattern[0]
		if prefix >= '1' && prefix <= '5' {
			return actual/100 == int(prefix-'0')
		}
		return false
	}
	// Exact match
	return strconv.Itoa(actual) == pattern
}

// HTTPConfig holds the configuration for HTTP checks.
type HTTPConfig struct {
	URL                 string            `json:"url"`
	Method              string            `json:"method,omitempty"`
	ExpectedStatus      int               `json:"expected_status,omitempty"`       //nolint:tagliatelle
	ExpectedStatusCodes []string          `json:"expected_status_codes,omitempty"` //nolint:tagliatelle
	Headers             map[string]string `json:"headers,omitempty"`
	Body                string            `json:"body,omitempty"`

	// Body pattern matching (simple string)
	BodyExpect string `json:"body_expect,omitempty"` //nolint:tagliatelle // API uses snake_case
	BodyReject string `json:"body_reject,omitempty"` //nolint:tagliatelle // API uses snake_case

	// Body pattern matching (regex)
	BodyPattern       string `json:"body_pattern,omitempty"`        //nolint:tagliatelle // API uses snake_case
	BodyPatternReject string `json:"body_pattern_reject,omitempty"` //nolint:tagliatelle // API uses snake_case

	// Header pattern matching (map of header name -> regex pattern)
	HeadersPattern map[string]string `json:"headers_pattern,omitempty"` //nolint:tagliatelle // API uses snake_case

	// BasicAuth holds a basic-auth credential as a single "user:pass" string.
	// It is the canonical, encrypted-at-rest home for basic auth: both halves
	// live behind one secret key. NormalizeConfig folds Username/Password into
	// it on every write.
	BasicAuth string `json:"basicAuth,omitempty"`

	// Legacy basic auth credentials, kept for rows written before the
	// BasicAuth fold and for manifests that spell the credential out (where
	// ${env:}/${param:} secret refs resolve into `password`). Execution falls
	// back to these when BasicAuth is empty — permanently, since migration is
	// lazy (rows fold on their next write).
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	// SecretHeaders holds header values that are encrypted at rest.
	// Use for API keys, Bearer tokens, and other sensitive header values.
	SecretHeaders map[string]string `json:"secretHeaders,omitempty"`

	// JSONPath assertions (AST-based validation of JSON response bodies)
	JSONPathAssertions *AssertionNode `json:"json_path_assertions,omitempty"` //nolint:tagliatelle // API uses snake_case

	// VerifySsl controls TLS certificate verification. nil (the vast majority
	// of stored configs) means "unset", which behaves like true — today's
	// hardcoded behavior. Only an explicit false disables verification, so
	// this must stay a pointer: a plain bool would make the zero value
	// indistinguishable from an explicit false.
	VerifySsl *bool `json:"verifySsl,omitempty"`

	// FollowRedirects controls whether the client follows HTTP redirects (up
	// to maxRedirects). nil means "unset", which behaves like true — today's
	// hardcoded behavior. Only an explicit false stops at the first
	// redirect response, so this must stay a pointer for the same reason as
	// VerifySsl.
	FollowRedirects *bool `json:"followRedirects,omitempty"`

	// CaptureFailureResponse opts this check into capturing what the probe
	// actually received when the check FAILS — status line, redacted response
	// headers and a size-capped body — for persistence as incident
	// diagnostics. Default false: response bodies can carry PII or session
	// material, so the org decides per check.
	//
	// A plain bool, not a pointer: unlike VerifySsl/FollowRedirects there is
	// no legacy "unset behaves like true" to preserve — off is off.
	CaptureFailureResponse bool `json:"capture_failure_response,omitempty"` //nolint:tagliatelle // API uses snake_case

	// Compiled regex patterns (not serialized, populated during validation)
	bodyPatternRegex       *regexp.Regexp            `json:"-"`
	bodyPatternRejectRegex *regexp.Regexp            `json:"-"`
	headersPatternRegex    map[string]*regexp.Regexp `json:"-"`
}

// resolveKey returns the value of the first matching key. The frontend writes
// camelCase by convention; the legacy snake_case keys are kept as a read
// fallback so previously stored configs keep working until the data fixer runs.
func resolveKey(configMap map[string]any, keys ...string) (any, string, bool) {
	for _, key := range keys {
		if v, ok := configMap[key]; ok && v != nil {
			return v, key, true
		}
	}

	return nil, "", false
}

// FromMap populates the configuration from a map.
//
//nolint:gocognit,cyclop,funlen // Config parsing requires handling many optional fields
func (c *HTTPConfig) FromMap(configMap map[string]any) error {
	// Extract URL (required)
	if url, ok := configMap[checkerdef.OutputKeyURL].(string); ok {
		c.URL = url
	} else if configMap[checkerdef.OutputKeyURL] != nil {
		return checkerdef.NewConfigError(checkerdef.OutputKeyURL, "must be a string")
	}

	// Extract Method (optional)
	if method, ok := configMap["method"].(string); ok {
		c.Method = method
	} else if configMap["method"] != nil {
		return checkerdef.NewConfigError("method", "must be a string")
	}

	// Extract ExpectedStatus (optional). Accept camelCase (canonical) and
	// snake_case (legacy / stored configs).
	if v, key, ok := resolveKey(configMap, "expectedStatus", "expected_status"); ok {
		switch typed := v.(type) {
		case int:
			c.ExpectedStatus = typed
		case float64:
			c.ExpectedStatus = int(typed)
		default:
			return checkerdef.NewConfigError(key, "must be a number")
		}
	}

	// Extract ExpectedStatusCodes (optional). Same camel/snake fallback.
	if v, key, ok := resolveKey(configMap, "expectedStatusCodes", "expected_status_codes"); ok {
		switch typed := v.(type) {
		case []string:
			c.ExpectedStatusCodes = typed
		case []any:
			c.ExpectedStatusCodes = make([]string, 0, len(typed))
			for i, item := range typed {
				if strVal, ok := item.(string); ok {
					c.ExpectedStatusCodes = append(c.ExpectedStatusCodes, strVal)
				} else {
					return checkerdef.NewConfigErrorf(key, "element %d must be a string", i)
				}
			}
		default:
			return checkerdef.NewConfigError(key, "must be a string array")
		}
	}

	// Extract Headers (optional)
	if headers, ok := configMap["headers"].(map[string]string); ok {
		c.Headers = headers
	} else if headersAny, ok := configMap["headers"].(map[string]any); ok {
		// Handle map[string]any and convert to map[string]string
		c.Headers = make(map[string]string, len(headersAny))
		for k, v := range headersAny {
			if strVal, ok := v.(string); ok {
				c.Headers[k] = strVal
			} else {
				return checkerdef.NewConfigErrorf("headers", "%s must be a string", k)
			}
		}
	} else if configMap["headers"] != nil {
		return checkerdef.NewConfigError("headers", "must be a map[string]string")
	}

	// Extract Body (optional)
	if body, ok := configMap["body"].(string); ok {
		c.Body = body
	} else if configMap["body"] != nil {
		return checkerdef.NewConfigError("body", "must be a string")
	}

	// Extract BodyExpect (optional)
	if v, key, ok := resolveKey(configMap, "bodyExpect", "body_expect"); ok {
		if s, ok := v.(string); ok {
			c.BodyExpect = s
		} else {
			return checkerdef.NewConfigError(key, "must be a string")
		}
	}

	// Extract BodyReject (optional)
	if v, key, ok := resolveKey(configMap, "bodyReject", "body_reject"); ok {
		if s, ok := v.(string); ok {
			c.BodyReject = s
		} else {
			return checkerdef.NewConfigError(key, "must be a string")
		}
	}

	// Extract BodyPattern (optional)
	if v, key, ok := resolveKey(configMap, "bodyPattern", "body_pattern"); ok {
		if s, ok := v.(string); ok {
			c.BodyPattern = s
		} else {
			return checkerdef.NewConfigError(key, "must be a string")
		}
	}

	// Extract BodyPatternReject (optional)
	if v, key, ok := resolveKey(configMap, "bodyPatternReject", "body_pattern_reject"); ok {
		if s, ok := v.(string); ok {
			c.BodyPatternReject = s
		} else {
			return checkerdef.NewConfigError(key, "must be a string")
		}
	}

	// Extract HeadersPattern (optional)
	if v, key, ok := resolveKey(configMap, "headersPattern", "headers_pattern"); ok {
		switch typed := v.(type) {
		case map[string]string:
			c.HeadersPattern = typed
		case map[string]any:
			c.HeadersPattern = make(map[string]string, len(typed))
			for k, item := range typed {
				if strVal, ok := item.(string); ok {
					c.HeadersPattern[k] = strVal
				} else {
					return checkerdef.NewConfigErrorf(key, "%s must be a string", k)
				}
			}
		default:
			return checkerdef.NewConfigError(key, "must be a map[string]string")
		}
	}

	// Extract BasicAuth (optional)
	if basicAuth, ok := configMap[configKeyBasicAuth].(string); ok {
		c.BasicAuth = basicAuth
	} else if configMap[configKeyBasicAuth] != nil {
		return checkerdef.NewConfigError(configKeyBasicAuth, "must be a string")
	}

	// Extract Username (optional, legacy)
	if username, ok := configMap[configKeyUsername].(string); ok {
		c.Username = username
	} else if configMap[configKeyUsername] != nil {
		return checkerdef.NewConfigError(configKeyUsername, "must be a string")
	}

	// Extract Password (optional, legacy)
	if password, ok := configMap[configKeyPassword].(string); ok {
		c.Password = password
	} else if configMap[configKeyPassword] != nil {
		return checkerdef.NewConfigError(configKeyPassword, "must be a string")
	}

	// Extract SecretHeaders (optional)
	if secretHeaders, ok := configMap[configKeySecretHeaders].(map[string]string); ok {
		c.SecretHeaders = secretHeaders
	} else if secretHeadersAny, ok := configMap[configKeySecretHeaders].(map[string]any); ok {
		c.SecretHeaders = make(map[string]string, len(secretHeadersAny))
		for k, v := range secretHeadersAny {
			if strVal, ok := v.(string); ok {
				c.SecretHeaders[k] = strVal
			} else {
				return checkerdef.NewConfigErrorf(configKeySecretHeaders, "%s must be a string", k)
			}
		}
	} else if configMap[configKeySecretHeaders] != nil {
		return checkerdef.NewConfigError(configKeySecretHeaders, "must be a map[string]string")
	}

	// Extract JSONPathAssertions (optional)
	if v, key, ok := resolveKey(configMap, "jsonPathAssertions", "json_path_assertions"); ok {
		node, err := parseAssertionNode(v)
		if err != nil {
			return checkerdef.NewConfigError(key, err.Error())
		}
		c.JSONPathAssertions = node
	}

	// Extract VerifySsl (optional). Presence-aware: only an explicit boolean
	// value overrides the true default, so a value must be parsed into a
	// pointer rather than a plain bool.
	if v, key, ok := resolveKey(configMap, "verifySsl", "verify_ssl"); ok {
		b, ok := v.(bool)
		if !ok {
			return checkerdef.NewConfigError(key, "must be a boolean")
		}
		c.VerifySsl = &b
	}

	// Extract FollowRedirects (optional). Presence-aware, same reasoning as
	// VerifySsl.
	if v, key, ok := resolveKey(configMap, "followRedirects", "follow_redirects"); ok {
		b, ok := v.(bool)
		if !ok {
			return checkerdef.NewConfigError(key, "must be a boolean")
		}
		c.FollowRedirects = &b
	}

	// Extract CaptureFailureResponse (optional). Canonical key is the
	// snake_case one; the camelCase spelling is accepted so a config written
	// by the frontend's usual convention still parses.
	if v, key, ok := resolveKey(configMap, "capture_failure_response", "captureFailureResponse"); ok {
		b, ok := v.(bool)
		if !ok {
			return checkerdef.NewConfigError(key, "must be a boolean")
		}
		c.CaptureFailureResponse = b
	}

	return nil
}

var (
	errAssertionNotObject = errors.New("must be an object")
	errAssertionNoType    = errors.New("type field is required")
)

// parseAssertionNode recursively parses a map into an AssertionNode.
func parseAssertionNode(raw any) (*AssertionNode, error) {
	nodeMap, ok := raw.(map[string]any)
	if !ok {
		return nil, errAssertionNotObject
	}

	node := &AssertionNode{}

	if t, ok := nodeMap["type"].(string); ok {
		node.Type = AssertionNodeType(t)
	} else {
		return nil, errAssertionNoType
	}

	if path, ok := nodeMap["path"].(string); ok {
		node.Path = path
	}

	if operator, ok := nodeMap["operator"].(string); ok {
		node.Operator = operator
	}

	if value, ok := nodeMap["value"].(string); ok {
		node.Value = value
	}

	if children, ok := nodeMap["children"].([]any); ok {
		node.Children = make([]AssertionNode, 0, len(children))
		for _, child := range children {
			childNode, err := parseAssertionNode(child)
			if err != nil {
				return nil, err
			}
			node.Children = append(node.Children, *childNode)
		}
	}

	return node, nil
}

// GetConfig implements the GetConfig interface by returning the configuration as a map.
//
//nolint:cyclop // HTTP config has many optional fields; branching is inherently verbose
func (c *HTTPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyURL: c.URL,
	}

	if c.Method != "" {
		cfg["method"] = c.Method
	}

	if c.ExpectedStatus != 0 {
		cfg["expectedStatus"] = c.ExpectedStatus
	}

	if len(c.ExpectedStatusCodes) > 0 {
		cfg["expectedStatusCodes"] = c.ExpectedStatusCodes
	}

	if len(c.Headers) > 0 {
		cfg["headers"] = c.Headers
	}

	if c.Body != "" {
		cfg["body"] = c.Body
	}

	if c.BodyExpect != "" {
		cfg["bodyExpect"] = c.BodyExpect
	}

	if c.BodyReject != "" {
		cfg["bodyReject"] = c.BodyReject
	}

	if c.BodyPattern != "" {
		cfg["bodyPattern"] = c.BodyPattern
	}

	if c.BodyPatternReject != "" {
		cfg["bodyPatternReject"] = c.BodyPatternReject
	}

	if len(c.HeadersPattern) > 0 {
		cfg["headersPattern"] = c.HeadersPattern
	}

	if c.BasicAuth != "" {
		cfg[configKeyBasicAuth] = c.BasicAuth
	}

	if c.Username != "" {
		cfg[configKeyUsername] = c.Username
	}

	if c.Password != "" {
		cfg[configKeyPassword] = c.Password
	}

	if len(c.SecretHeaders) > 0 {
		cfg[configKeySecretHeaders] = c.SecretHeaders
	}

	if c.JSONPathAssertions != nil {
		// Serialize via JSON to produce map[string]any so that FromMap can parse it back.
		if b, err := json.Marshal(c.JSONPathAssertions); err == nil {
			var m any
			if err := json.Unmarshal(b, &m); err == nil {
				cfg["jsonPathAssertions"] = m
			}
		}
	}

	c.addToggleConfig(cfg)

	return cfg
}

// addToggleConfig writes the three boolean toggles, each omitted at its own
// default so an untouched config round-trips without ever gaining a key:
// VerifySsl / FollowRedirects default to ON and are written only at false;
// CaptureFailureResponse defaults to OFF and is written only at true.
func (c *HTTPConfig) addToggleConfig(cfg map[string]any) {
	if c.VerifySsl != nil && !*c.VerifySsl {
		cfg["verifySsl"] = false
	}

	if c.FollowRedirects != nil && !*c.FollowRedirects {
		cfg["followRedirects"] = false
	}

	if c.CaptureFailureResponse {
		cfg["capture_failure_response"] = true
	}
}

// SkipTLSVerify reports whether TLS certificate verification should be
// skipped for this check. Defaults to false (verify) when unset.
func (c *HTTPConfig) SkipTLSVerify() bool {
	return c.VerifySsl != nil && !*c.VerifySsl
}

// SkipRedirects reports whether the client should stop at the first redirect
// response instead of following it. Defaults to false (follow) when unset.
func (c *HTTPConfig) SkipRedirects() bool {
	return c.FollowRedirects != nil && !*c.FollowRedirects
}

// SecretFields declares which top-level config keys carry secrets and must
// be encrypted at rest. Implements credentials.SecretFielder.
//
// `password` stays listed even though NormalizeConfig folds it away: legacy
// rows written before the fold still carry one, and it must keep being
// encrypted and preserved across a PATCH that doesn't mention it.
//
// `username` is deliberately NOT listed: it would be stripped from exports and
// swept into the encrypted blob by credmigrate. The fold is what protects it —
// once folded, the username lives inside the encrypted `basicAuth` value.
func (c *HTTPConfig) SecretFields() []string {
	return []string{configKeyBasicAuth, configKeyPassword, configKeySecretHeaders}
}

// stringConfigValue reads a string key from a config map, tolerating an absent
// or nil value and rejecting a wrong type with a *checkerdef.ConfigError.
func stringConfigValue(configMap map[string]any, key string) (string, error) {
	raw, ok := configMap[key]
	if !ok || raw == nil {
		return "", nil
	}

	str, ok := raw.(string)
	if !ok {
		return "", checkerdef.NewConfigError(key, "must be a string")
	}

	return str, nil
}

// NormalizeConfig folds the legacy top-level `username`/`password` pair into the
// single reserved, encrypted `basicAuth` key. Implements
// checkerdef.ConfigNormalizer; it runs on the EFFECTIVE (post-merge) config,
// never on a raw PATCH body.
//
// Rules:
//   - `username`/`password` are always removed, so no stray plaintext half is
//     left behind on the public config and clearing actually clears.
//   - a non-empty `username` overwrites any preserved `basicAuth` — that is what
//     keeps re-applying the same manifest idempotent.
//   - the fold is gated on `username != ""` exactly, mirroring execution: a
//     password-only config sends no auth header, so it stores no credential and
//     its stray password is dropped.
//   - an absent `username` leaves a preserved `basicAuth` untouched, so an
//     unrelated PATCH keeps the credential.
func (c *HTTPConfig) NormalizeConfig(configMap map[string]any) (map[string]any, error) {
	normalized := make(map[string]any, len(configMap))
	for key, value := range configMap {
		normalized[key] = value
	}

	username, err := stringConfigValue(normalized, configKeyUsername)
	if err != nil {
		return nil, err
	}

	password, err := stringConfigValue(normalized, configKeyPassword)
	if err != nil {
		return nil, err
	}

	delete(normalized, configKeyUsername)
	delete(normalized, configKeyPassword)

	if username == "" {
		return normalized, nil
	}

	// RFC 7617: the user-id must not contain a colon, or the credential
	// decodes ambiguously.
	if strings.Contains(username, basicAuthSeparator) {
		return nil, checkerdef.NewConfigError(configKeyUsername, "must not contain a ':' character")
	}

	normalized[configKeyBasicAuth] = username + basicAuthSeparator + password

	return normalized, nil
}

// BasicAuthCredentials returns the effective basic-auth credential, preferring
// the folded `basicAuth` value and falling back to the legacy
// `username`/`password` pair (which lazily-unmigrated rows still carry). The
// boolean reports whether any credential is configured at all.
func (c *HTTPConfig) BasicAuthCredentials() (string, string, bool) {
	if c.BasicAuth != "" {
		username, password, _ := strings.Cut(c.BasicAuth, basicAuthSeparator)

		return username, password, true
	}

	if c.Username != "" {
		return c.Username, c.Password, true
	}

	return "", "", false
}
