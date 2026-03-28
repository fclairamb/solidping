package checkhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/version"
)

// Status pattern validation errors.
var (
	errPatternEmpty    = errors.New("pattern cannot be empty")
	errInvalidWildcard = errors.New("invalid wildcard pattern: prefix must be 1-5")
	errInvalidPattern  = errors.New("pattern must be a number or wildcard like 2XX")
	errStatusCodeRange = errors.New("status code must be between 100 and 599")
)

// validateStatusPattern validates a single status code pattern.
// Valid patterns: exact codes like "200", "404", or wildcards like "2XX", "3XX".
func validateStatusPattern(pattern string) error {
	pattern = strings.ToUpper(strings.TrimSpace(pattern))
	if pattern == "" {
		return errPatternEmpty
	}

	// Check for wildcard pattern (e.g., "2XX")
	if strings.HasSuffix(pattern, "XX") && len(pattern) == 3 {
		prefix := pattern[0]
		if prefix >= '1' && prefix <= '5' {
			return nil
		}

		return fmt.Errorf("%w: %s", errInvalidWildcard, pattern)
	}

	// Check for exact status code
	code, err := strconv.Atoi(pattern)
	if err != nil {
		return fmt.Errorf("%w: %s", errInvalidPattern, pattern)
	}

	if code < 100 || code > 599 {
		return fmt.Errorf("%w: %d", errStatusCodeRange, code)
	}

	return nil
}

const (
	maxRedirects  = 10                          // Maximum number of HTTP redirects to follow
	maxBodySizeMB = 10                          // Maximum response body size in MB
	maxBodySize   = maxBodySizeMB * 1024 * 1024 // Maximum response body size in bytes
)

// HTTPChecker implements the Checker interface for HTTP checks.
type HTTPChecker struct{}

// Type returns the check type identifier.
func (c *HTTPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeHTTP
}

// Validate checks if the configuration is valid.
//
//nolint:cyclop,funlen,gocognit // Config validation requires checking many fields
func (c *HTTPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &HTTPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	// Validate URL
	if cfg.URL == "" {
		return checkerdef.NewConfigError("url", "is required")
	}

	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		return checkerdef.NewConfigError("url", "must start with http:// or https://")
	}

	parsedURL, err := url.Parse(cfg.URL)
	if err != nil {
		return checkerdef.NewConfigError("url", "invalid URL format")
	}

	// Auto-generate name and slug from URL if not provided
	if spec.Name == "" || spec.Slug == "" {
		// Extract hostname (without port)
		hostname := parsedURL.Hostname()

		// Set name to hostname if empty
		if spec.Name == "" {
			spec.Name = hostname
		}

		// Set slug to hostname with dots replaced by hyphens if empty
		if spec.Slug == "" {
			spec.Slug = strings.ReplaceAll(hostname, ".", "-")
		}
	}

	// Validate HTTP method
	if cfg.Method != "" {
		validMethods := map[string]bool{
			http.MethodGet:     true,
			http.MethodPost:    true,
			http.MethodPut:     true,
			http.MethodDelete:  true,
			http.MethodHead:    true,
			http.MethodOptions: true,
			http.MethodPatch:   true,
		}

		method := strings.ToUpper(cfg.Method)
		if !validMethods[method] {
			return checkerdef.NewConfigErrorf("method", "invalid HTTP method: %s", cfg.Method)
		}
	}

	// Validate expected status (deprecated, but still supported)
	if cfg.ExpectedStatus != 0 && (cfg.ExpectedStatus < 100 || cfg.ExpectedStatus > 599) {
		return checkerdef.NewConfigErrorf("expected_status", "must be between 100 and 599, got %d", cfg.ExpectedStatus)
	}

	// Validate expected status codes patterns
	for i, pattern := range cfg.ExpectedStatusCodes {
		if err := validateStatusPattern(pattern); err != nil {
			return checkerdef.NewConfigErrorf("expected_status_codes", "element %d: %v", i, err)
		}
	}

	// Compile and validate regex patterns
	if cfg.BodyPattern != "" {
		regex, err := regexp.Compile(cfg.BodyPattern)
		if err != nil {
			return checkerdef.NewConfigErrorf("body_pattern", "invalid regex pattern: %v", err)
		}
		cfg.bodyPatternRegex = regex
	}

	if cfg.BodyPatternReject != "" {
		regex, err := regexp.Compile(cfg.BodyPatternReject)
		if err != nil {
			return checkerdef.NewConfigErrorf("body_pattern_reject", "invalid regex pattern: %v", err)
		}
		cfg.bodyPatternRejectRegex = regex
	}

	if len(cfg.HeadersPattern) > 0 {
		cfg.headersPatternRegex = make(map[string]*regexp.Regexp, len(cfg.HeadersPattern))
		for headerName, pattern := range cfg.HeadersPattern {
			regex, err := regexp.Compile(pattern)
			if err != nil {
				return checkerdef.NewConfigErrorf("headers_pattern", "invalid regex pattern for header %q: %v", headerName, err)
			}
			cfg.headersPatternRegex[headerName] = regex
		}
	}

	// Validate JSONPath assertions
	if cfg.JSONPathAssertions != nil {
		if err := cfg.JSONPathAssertions.Validate(); err != nil {
			return checkerdef.NewConfigError("json_path_assertions", err.Error())
		}
	}

	return nil
}

// Execute performs the HTTP check and returns the result.
//
//nolint:funlen,gocognit,cyclop // HTTP checking with pattern matching requires comprehensive validation
func (c *HTTPChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*HTTPConfig](config)
	if err != nil {
		return nil, err
	}

	// Apply defaults
	method := cfg.Method
	if method == "" {
		method = http.MethodGet
	} else {
		method = strings.ToUpper(method)
	}

	expectedStatus := cfg.ExpectedStatus
	if expectedStatus == 0 {
		expectedStatus = 200
	}

	start := time.Now()

	// Create request with body if provided
	var bodyReader *strings.Reader
	if cfg.Body != "" {
		bodyReader = strings.NewReader(cfg.Body)
	}

	var req *http.Request

	if bodyReader != nil {
		req, err = http.NewRequestWithContext(ctx, method, cfg.URL, bodyReader)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, cfg.URL, nil)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add basic auth if configured (before headers, so explicit Authorization overrides)
	if cfg.Username != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}

	// Add default User-Agent header
	req.Header.Set("User-Agent", version.UserAgent)

	// Add custom headers (can override User-Agent and Authorization)
	for key, value := range cfg.Headers {
		req.Header.Set(key, value)
	}

	// Execute the request
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			// Allow up to maxRedirects redirects
			if len(via) >= maxRedirects {
				return http.ErrUseLastResponse
			}

			return nil
		},
	}

	resp, err := client.Do(req)
	duration := time.Since(start)

	if err != nil {
		// Check if context was canceled or timed out
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: duration,
				Output: map[string]any{
					"error": "request timed out",
					"url":   cfg.URL,
				},
			}, nil
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: duration,
			Output: map[string]any{
				"error": err.Error(),
				"url":   cfg.URL,
			},
		}, nil
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	// Compile regex patterns if not already compiled
	if cfg.BodyPattern != "" && cfg.bodyPatternRegex == nil {
		regex, err := regexp.Compile(cfg.BodyPattern)
		if err != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: duration,
				Output: map[string]any{
					"error":       fmt.Sprintf("invalid body_pattern regex: %v", err),
					"url":         cfg.URL,
					"status_code": resp.StatusCode,
					"method":      method,
				},
			}, nil
		}
		cfg.bodyPatternRegex = regex
	}

	if cfg.BodyPatternReject != "" && cfg.bodyPatternRejectRegex == nil {
		regex, err := regexp.Compile(cfg.BodyPatternReject)
		if err != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: duration,
				Output: map[string]any{
					"error":       fmt.Sprintf("invalid body_pattern_reject regex: %v", err),
					"url":         cfg.URL,
					"status_code": resp.StatusCode,
					"method":      method,
				},
			}, nil
		}
		cfg.bodyPatternRejectRegex = regex
	}

	if len(cfg.HeadersPattern) > 0 && len(cfg.headersPatternRegex) == 0 {
		cfg.headersPatternRegex = make(map[string]*regexp.Regexp, len(cfg.HeadersPattern))
		for headerName, pattern := range cfg.HeadersPattern {
			regex, err := regexp.Compile(pattern)
			if err != nil {
				return &checkerdef.Result{
					Status:   checkerdef.StatusDown,
					Duration: duration,
					Output: map[string]any{
						"error":       fmt.Sprintf("invalid headers_pattern regex for header %q: %v", headerName, err),
						"url":         cfg.URL,
						"status_code": resp.StatusCode,
						"method":      method,
					},
				}, nil
			}
			cfg.headersPatternRegex[headerName] = regex
		}
	}

	// Read response body if pattern matching is needed
	var respBody string
	if cfg.BodyExpect != "" || cfg.BodyReject != "" || cfg.BodyPattern != "" || cfg.BodyPatternReject != "" {
		// Limit body size to prevent memory issues
		limitedReader := io.LimitReader(resp.Body, maxBodySize)
		bodyBytes, err := io.ReadAll(limitedReader)
		if err != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: duration,
				Output: map[string]any{
					"error":       fmt.Sprintf("failed to read response body: %v", err),
					"url":         cfg.URL,
					"status_code": resp.StatusCode,
					"method":      method,
				},
			}, nil
		}
		respBody = string(bodyBytes)
	}

	// Apply body pattern matching
	if cfg.BodyExpect != "" {
		if !strings.Contains(respBody, cfg.BodyExpect) {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: duration,
				Metrics: map[string]any{
					"duration_ms": float64(duration.Microseconds()) / 1000.0,
				},
				Output: map[string]any{
					"error":       fmt.Sprintf("Expected string %q not found in response body", cfg.BodyExpect),
					"url":         cfg.URL,
					"status_code": resp.StatusCode,
					"method":      method,
				},
			}, nil
		}
	}

	if cfg.BodyReject != "" {
		if strings.Contains(respBody, cfg.BodyReject) {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: duration,
				Metrics: map[string]any{
					"duration_ms": float64(duration.Microseconds()) / 1000.0,
				},
				Output: map[string]any{
					"error":       fmt.Sprintf("Rejected string %q found in response body", cfg.BodyReject),
					"url":         cfg.URL,
					"status_code": resp.StatusCode,
					"method":      method,
				},
			}, nil
		}
	}

	if cfg.bodyPatternRegex != nil {
		if !cfg.bodyPatternRegex.MatchString(respBody) {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: duration,
				Metrics: map[string]any{
					"duration_ms": float64(duration.Microseconds()) / 1000.0,
				},
				Output: map[string]any{
					"error":       fmt.Sprintf("Expected pattern %q not found in response body", cfg.BodyPattern),
					"url":         cfg.URL,
					"status_code": resp.StatusCode,
					"method":      method,
				},
			}, nil
		}
	}

	if cfg.bodyPatternRejectRegex != nil {
		if cfg.bodyPatternRejectRegex.MatchString(respBody) {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: duration,
				Metrics: map[string]any{
					"duration_ms": float64(duration.Microseconds()) / 1000.0,
				},
				Output: map[string]any{
					"error":       fmt.Sprintf("Rejected pattern %q found in response body", cfg.BodyPatternReject),
					"url":         cfg.URL,
					"status_code": resp.StatusCode,
					"method":      method,
				},
			}, nil
		}
	}

	// Apply header pattern matching
	if len(cfg.headersPatternRegex) > 0 {
		for headerName, headerRegex := range cfg.headersPatternRegex {
			headerValue := resp.Header.Get(headerName)
			if headerValue == "" {
				return &checkerdef.Result{
					Status:   checkerdef.StatusDown,
					Duration: duration,
					Metrics: map[string]any{
						"duration_ms": float64(duration.Microseconds()) / 1000.0,
					},
					Output: map[string]any{
						"error":       fmt.Sprintf("Required header %q not found in response", headerName),
						"url":         cfg.URL,
						"status_code": resp.StatusCode,
						"method":      method,
					},
				}, nil
			}
			if !headerRegex.MatchString(headerValue) {
				errMsg := fmt.Sprintf(
					"Header %q value does not match pattern %q",
					headerName, cfg.HeadersPattern[headerName],
				)
				return &checkerdef.Result{
					Status:   checkerdef.StatusDown,
					Duration: duration,
					Metrics: map[string]any{
						"duration_ms": float64(duration.Microseconds()) / 1000.0,
					},
					Output: map[string]any{
						"error":       errMsg,
						"url":         cfg.URL,
						"status_code": resp.StatusCode,
						"method":      method,
					},
				}, nil
			}
		}
	}

	// Apply JSONPath assertions
	if cfg.JSONPathAssertions != nil && respBody != "" {
		var jsonData any
		if unmarshalErr := json.Unmarshal([]byte(respBody), &jsonData); unmarshalErr != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: duration,
				Metrics: map[string]any{
					"duration_ms": float64(duration.Microseconds()) / 1000.0,
				},
				Output: map[string]any{
					"error":       "response body is not valid JSON for assertion evaluation",
					"url":         cfg.URL,
					"status_code": resp.StatusCode,
					"method":      method,
				},
			}, nil
		}

		assertionResult := cfg.JSONPathAssertions.Evaluate(jsonData)
		if !assertionResult.Pass {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: duration,
				Metrics: map[string]any{
					"duration_ms": float64(duration.Microseconds()) / 1000.0,
				},
				Output: map[string]any{
					"error":                "JSON assertion failed",
					"url":                  cfg.URL,
					"status_code":          resp.StatusCode,
					"method":               method,
					"json_path_assertions": assertionResult,
				},
			}, nil
		}
	}

	// Determine status based on expected status code(s)
	status := checkerdef.StatusUp
	if len(cfg.ExpectedStatusCodes) > 0 {
		// Use new pattern-based matching
		if !MatchStatusCode(resp.StatusCode, cfg.ExpectedStatusCodes) {
			status = checkerdef.StatusDown
		}
	} else {
		// Fall back to legacy expectedStatus (default: 200)
		if resp.StatusCode != expectedStatus {
			status = checkerdef.StatusDown
		}
	}

	return &checkerdef.Result{
		Status:   status,
		Duration: duration,
		Metrics: map[string]any{
			"duration_ms": float64(duration.Microseconds()) / 1000.0,
		},
		Output: map[string]any{
			"url":         cfg.URL,
			"status_code": resp.StatusCode,
			"method":      method,
		},
	}, nil
}
