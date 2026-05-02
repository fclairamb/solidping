package checkhttp

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
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

	// Basic auth credentials
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	// JSONPath assertions (AST-based validation of JSON response bodies)
	JSONPathAssertions *AssertionNode `json:"json_path_assertions,omitempty"` //nolint:tagliatelle // API uses snake_case

	// Compiled regex patterns (not serialized, populated during validation)
	bodyPatternRegex       *regexp.Regexp            `json:"-"`
	bodyPatternRejectRegex *regexp.Regexp            `json:"-"`
	headersPatternRegex    map[string]*regexp.Regexp `json:"-"`
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

	// Extract ExpectedStatus (optional, deprecated in favor of expected_status_codes)
	if expectedStatus, ok := configMap["expected_status"].(int); ok {
		c.ExpectedStatus = expectedStatus
	} else if expectedStatusFloat, ok := configMap["expected_status"].(float64); ok {
		// Handle JSON numbers which unmarshal as float64
		c.ExpectedStatus = int(expectedStatusFloat)
	} else if configMap["expected_status"] != nil {
		return checkerdef.NewConfigError("expected_status", "must be a number")
	}

	// Extract ExpectedStatusCodes (optional)
	if statusCodes, ok := configMap["expected_status_codes"].([]string); ok {
		c.ExpectedStatusCodes = statusCodes
	} else if statusCodesAny, ok := configMap["expected_status_codes"].([]any); ok {
		// Handle []any from JSON unmarshaling
		c.ExpectedStatusCodes = make([]string, 0, len(statusCodesAny))
		for i, v := range statusCodesAny {
			if strVal, ok := v.(string); ok {
				c.ExpectedStatusCodes = append(c.ExpectedStatusCodes, strVal)
			} else {
				return checkerdef.NewConfigErrorf("expected_status_codes", "element %d must be a string", i)
			}
		}
	} else if configMap["expected_status_codes"] != nil {
		return checkerdef.NewConfigError("expected_status_codes", "must be a string array")
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
	if bodyExpect, ok := configMap["body_expect"].(string); ok {
		c.BodyExpect = bodyExpect
	} else if configMap["body_expect"] != nil {
		return checkerdef.NewConfigError("body_expect", "must be a string")
	}

	// Extract BodyReject (optional)
	if bodyReject, ok := configMap["body_reject"].(string); ok {
		c.BodyReject = bodyReject
	} else if configMap["body_reject"] != nil {
		return checkerdef.NewConfigError("body_reject", "must be a string")
	}

	// Extract BodyPattern (optional)
	if bodyPattern, ok := configMap["body_pattern"].(string); ok {
		c.BodyPattern = bodyPattern
	} else if configMap["body_pattern"] != nil {
		return checkerdef.NewConfigError("body_pattern", "must be a string")
	}

	// Extract BodyPatternReject (optional)
	if bodyPatternReject, ok := configMap["body_pattern_reject"].(string); ok {
		c.BodyPatternReject = bodyPatternReject
	} else if configMap["body_pattern_reject"] != nil {
		return checkerdef.NewConfigError("body_pattern_reject", "must be a string")
	}

	// Extract HeadersPattern (optional)
	if headersPattern, ok := configMap["headers_pattern"].(map[string]string); ok {
		c.HeadersPattern = headersPattern
	} else if headersPatternAny, ok := configMap["headers_pattern"].(map[string]any); ok {
		// Handle map[string]any and convert to map[string]string
		c.HeadersPattern = make(map[string]string, len(headersPatternAny))
		for k, v := range headersPatternAny {
			if strVal, ok := v.(string); ok {
				c.HeadersPattern[k] = strVal
			} else {
				return checkerdef.NewConfigErrorf("headers_pattern", "%s must be a string", k)
			}
		}
	} else if configMap["headers_pattern"] != nil {
		return checkerdef.NewConfigError("headers_pattern", "must be a map[string]string")
	}

	// Extract Username (optional)
	if username, ok := configMap["username"].(string); ok {
		c.Username = username
	} else if configMap["username"] != nil {
		return checkerdef.NewConfigError("username", "must be a string")
	}

	// Extract Password (optional)
	if password, ok := configMap["password"].(string); ok {
		c.Password = password
	} else if configMap["password"] != nil {
		return checkerdef.NewConfigError("password", "must be a string")
	}

	// Extract JSONPathAssertions (optional)
	if assertions, ok := configMap["json_path_assertions"]; ok && assertions != nil {
		node, err := parseAssertionNode(assertions)
		if err != nil {
			return checkerdef.NewConfigError("json_path_assertions", err.Error())
		}
		c.JSONPathAssertions = node
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
func (c *HTTPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyURL: c.URL,
	}

	if c.Method != "" {
		cfg["method"] = c.Method
	}

	if c.ExpectedStatus != 0 {
		cfg["expected_status"] = c.ExpectedStatus
	}

	if len(c.ExpectedStatusCodes) > 0 {
		cfg["expected_status_codes"] = c.ExpectedStatusCodes
	}

	if len(c.Headers) > 0 {
		cfg["headers"] = c.Headers
	}

	if c.Body != "" {
		cfg["body"] = c.Body
	}

	if c.BodyExpect != "" {
		cfg["body_expect"] = c.BodyExpect
	}

	if c.BodyReject != "" {
		cfg["body_reject"] = c.BodyReject
	}

	if c.BodyPattern != "" {
		cfg["body_pattern"] = c.BodyPattern
	}

	if c.BodyPatternReject != "" {
		cfg["body_pattern_reject"] = c.BodyPatternReject
	}

	if len(c.HeadersPattern) > 0 {
		cfg["headers_pattern"] = c.HeadersPattern
	}

	if c.Username != "" {
		cfg["username"] = c.Username
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	if c.JSONPathAssertions != nil {
		cfg["json_path_assertions"] = c.JSONPathAssertions
	}

	return cfg
}

// SecretFields declares which top-level config keys carry secrets and must
// be encrypted at rest. Implements credentials.SecretFielder. V1 covers the
// basic-auth password only; Authorization headers and bearer tokens passed
// inside `headers` are a known V2 follow-up (see credentials-encryption spec).
func (c *HTTPConfig) SecretFields() []string {
	return []string{"password"}
}
