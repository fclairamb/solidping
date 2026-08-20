//nolint:usestdlibvars // Test files use standard Go test patterns
package checkhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/version"
)

func TestHTTPConfig_FromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   map[string]any
		want    HTTPConfig
		wantErr bool
	}{
		{
			name: "full config",
			input: map[string]any{
				"url":             "http://example.com",
				"method":          "POST",
				"expected_status": float64(201), // JSON numbers unmarshal as float64
				"headers": map[string]any{
					"Authorization": "Bearer token",
					"Content-Type":  "application/json",
				},
				"body": `{"test": "data"}`,
			},
			want: HTTPConfig{
				URL:            "http://example.com",
				Method:         "POST",
				ExpectedStatus: 201,
				Headers: map[string]string{
					"Authorization": "Bearer token",
					"Content-Type":  "application/json",
				},
				Body: `{"test": "data"}`,
			},
			wantErr: false,
		},
		{
			name: "minimal config",
			input: map[string]any{
				"url": "https://example.com",
			},
			want: HTTPConfig{
				URL: "https://example.com",
			},
			wantErr: false,
		},
		{
			name: "expected_status as int",
			input: map[string]any{
				"url":             "http://example.com",
				"expected_status": 200,
			},
			want: HTTPConfig{
				URL:            "http://example.com",
				ExpectedStatus: 200,
			},
			wantErr: false,
		},
		{
			name: "headers as map[string]string",
			input: map[string]any{
				"url": "http://example.com",
				"headers": map[string]string{
					"X-Custom": "value",
				},
			},
			want: HTTPConfig{
				URL: "http://example.com",
				Headers: map[string]string{
					"X-Custom": "value",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid url type",
			input: map[string]any{
				"url": 123,
			},
			wantErr: true,
		},
		{
			name: "invalid method type",
			input: map[string]any{
				"url":    "http://example.com",
				"method": 123,
			},
			wantErr: true,
		},
		{
			name: "invalid expected_status type",
			input: map[string]any{
				"url":             "http://example.com",
				"expected_status": "200",
			},
			wantErr: true,
		},
		{
			name: "invalid headers type",
			input: map[string]any{
				"url":     "http://example.com",
				"headers": "invalid",
			},
			wantErr: true,
		},
		{
			name: "invalid header value type",
			input: map[string]any{
				"url": "http://example.com",
				"headers": map[string]any{
					"X-Custom": 123,
				},
			},
			wantErr: true,
		},
		{
			name: "invalid body type",
			input: map[string]any{
				"url":  "http://example.com",
				"body": 123,
			},
			wantErr: true,
		},
		{
			name: "body pattern matching fields",
			input: map[string]any{
				"url":                 "http://example.com",
				"body_expect":         "success",
				"body_reject":         "error",
				"body_pattern":        "status: \\w+",
				"body_pattern_reject": "(error|fail)",
			},
			want: HTTPConfig{
				URL:               "http://example.com",
				BodyExpect:        "success",
				BodyReject:        "error",
				BodyPattern:       "status: \\w+",
				BodyPatternReject: "(error|fail)",
			},
			wantErr: false,
		},
		{
			name: "headers pattern matching",
			input: map[string]any{
				"url": "http://example.com",
				"headers_pattern": map[string]any{
					"Content-Type":  "application/json",
					"X-API-Version": "v\\d+",
				},
			},
			want: HTTPConfig{
				URL: "http://example.com",
				HeadersPattern: map[string]string{
					"Content-Type":  "application/json",
					"X-API-Version": "v\\d+",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid body_expect type",
			input: map[string]any{
				"url":         "http://example.com",
				"body_expect": 123,
			},
			wantErr: true,
		},
		{
			name: "invalid body_reject type",
			input: map[string]any{
				"url":         "http://example.com",
				"body_reject": 123,
			},
			wantErr: true,
		},
		{
			name: "invalid body_pattern type",
			input: map[string]any{
				"url":          "http://example.com",
				"body_pattern": 123,
			},
			wantErr: true,
		},
		{
			name: "invalid body_pattern_reject type",
			input: map[string]any{
				"url":                 "http://example.com",
				"body_pattern_reject": 123,
			},
			wantErr: true,
		},
		{
			name: "invalid headers_pattern type",
			input: map[string]any{
				"url":             "http://example.com",
				"headers_pattern": "invalid",
			},
			wantErr: true,
		},
		{
			name: "invalid headers_pattern value type",
			input: map[string]any{
				"url": "http://example.com",
				"headers_pattern": map[string]any{
					"Content-Type": 123,
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var cfg HTTPConfig
			err := cfg.FromMap(tt.input)

			if (err != nil) != tt.wantErr {
				t.Errorf("HTTPConfig.FromMap() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return // Expected error, test passed
			}

			// Verify all fields
			if cfg.URL != tt.want.URL {
				t.Errorf("URL = %v, want %v", cfg.URL, tt.want.URL)
			}
			if cfg.Method != tt.want.Method {
				t.Errorf("Method = %v, want %v", cfg.Method, tt.want.Method)
			}
			if cfg.ExpectedStatus != tt.want.ExpectedStatus {
				t.Errorf("ExpectedStatus = %v, want %v", cfg.ExpectedStatus, tt.want.ExpectedStatus)
			}
			if cfg.Body != tt.want.Body {
				t.Errorf("Body = %v, want %v", cfg.Body, tt.want.Body)
			}

			// Verify pattern matching fields
			if cfg.BodyExpect != tt.want.BodyExpect {
				t.Errorf("BodyExpect = %v, want %v", cfg.BodyExpect, tt.want.BodyExpect)
			}
			if cfg.BodyReject != tt.want.BodyReject {
				t.Errorf("BodyReject = %v, want %v", cfg.BodyReject, tt.want.BodyReject)
			}
			if cfg.BodyPattern != tt.want.BodyPattern {
				t.Errorf("BodyPattern = %v, want %v", cfg.BodyPattern, tt.want.BodyPattern)
			}
			if cfg.BodyPatternReject != tt.want.BodyPatternReject {
				t.Errorf("BodyPatternReject = %v, want %v", cfg.BodyPatternReject, tt.want.BodyPatternReject)
			}

			// Verify headers
			if len(cfg.Headers) != len(tt.want.Headers) {
				t.Errorf("Headers length = %v, want %v", len(cfg.Headers), len(tt.want.Headers))
			}
			for k, v := range tt.want.Headers {
				if cfg.Headers[k] != v {
					t.Errorf("Headers[%s] = %v, want %v", k, cfg.Headers[k], v)
				}
			}

			// Verify headers pattern
			if len(cfg.HeadersPattern) != len(tt.want.HeadersPattern) {
				t.Errorf("HeadersPattern length = %v, want %v", len(cfg.HeadersPattern), len(tt.want.HeadersPattern))
			}
			for k, v := range tt.want.HeadersPattern {
				if cfg.HeadersPattern[k] != v {
					t.Errorf("HeadersPattern[%s] = %v, want %v", k, cfg.HeadersPattern[k], v)
				}
			}
		})
	}
}

func TestHTTPConfig_GetConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   HTTPConfig
		expected map[string]any
	}{
		{
			name: "full config",
			config: HTTPConfig{
				URL:            "http://example.com",
				Method:         "POST",
				ExpectedStatus: 201,
				Headers: map[string]string{
					"Authorization": "Bearer token",
				},
				Body: `{"test": "data"}`,
			},
			expected: map[string]any{
				"url":            "http://example.com",
				"method":         "POST",
				"expectedStatus": 201,
				"headers": map[string]string{
					"Authorization": "Bearer token",
				},
				"body": `{"test": "data"}`,
			},
		},
		{
			name: "minimal config",
			config: HTTPConfig{
				URL: "https://example.com",
			},
			expected: map[string]any{
				"url": "https://example.com",
			},
		},
		{
			name: "partial config",
			config: HTTPConfig{
				URL:    "http://example.com",
				Method: "GET",
			},
			expected: map[string]any{
				"url":    "http://example.com",
				"method": "GET",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.config.GetConfig()

			// Check that all expected keys are present
			for key, expectedValue := range tt.expected {
				gotValue, ok := got[key]
				if !ok {
					t.Errorf("GetConfig() missing key %q", key)
					continue
				}

				// Special handling for maps
				if expectedMap, ok := expectedValue.(map[string]string); ok {
					gotMap, ok := gotValue.(map[string]string)
					if !ok {
						t.Errorf("GetConfig()[%q] type mismatch, expected map[string]string, got %T", key, gotValue)
						continue
					}
					if len(gotMap) != len(expectedMap) {
						t.Errorf("GetConfig()[%q] length = %d, want %d", key, len(gotMap), len(expectedMap))
					}
					for k, v := range expectedMap {
						if gotMap[k] != v {
							t.Errorf("GetConfig()[%q][%q] = %v, want %v", key, k, gotMap[k], v)
						}
					}
				} else if gotValue != expectedValue {
					t.Errorf("GetConfig()[%q] = %v, want %v", key, gotValue, expectedValue)
				}
			}

			// Check that no unexpected keys are present
			for key := range got {
				if _, ok := tt.expected[key]; !ok {
					t.Errorf("GetConfig() has unexpected key %q", key)
				}
			}
		})
	}
}

func TestHTTPConfig_RoundTrip(t *testing.T) {
	t.Parallel()
	// Test that FromMap and GetConfig are inverses
	original := map[string]any{
		"url":             "http://example.com",
		"method":          "POST",
		"expected_status": float64(201),
		"headers": map[string]any{
			"Authorization": "Bearer token",
		},
		"body": `{"test": "data"}`,
	}

	var cfg HTTPConfig
	if err := cfg.FromMap(original); err != nil {
		t.Fatalf("FromMap() error = %v", err)
	}

	result := cfg.GetConfig()

	// Verify key fields are preserved
	if result["url"] != "http://example.com" {
		t.Errorf("url not preserved: got %v", result["url"])
	}
	if result["method"] != "POST" {
		t.Errorf("method not preserved: got %v", result["method"])
	}
	if result["expectedStatus"] != 201 {
		t.Errorf("expectedStatus not preserved: got %v", result["expectedStatus"])
	}
	if result["body"] != `{"test": "data"}` {
		t.Errorf("body not preserved: got %v", result["body"])
	}

	headers, ok := result["headers"].(map[string]string)
	if !ok {
		t.Errorf("headers type mismatch: got %T", result["headers"])
	} else if headers["Authorization"] != "Bearer token" {
		t.Errorf("headers not preserved: got %v", headers)
	}
}

func TestHTTPChecker_Type(t *testing.T) {
	t.Parallel()
	checker := &HTTPChecker{}
	if got := checker.Type(); got != checkerdef.CheckTypeHTTP {
		t.Errorf("HTTPChecker.Type() = %v, want %v", got, checkerdef.CheckTypeHTTP)
	}
}

func TestHTTPChecker_Validate(t *testing.T) {
	t.Parallel()
	checker := &HTTPChecker{}

	tests := []struct {
		name    string
		config  *HTTPConfig
		wantErr bool
	}{
		{
			name: "valid http config",
			config: &HTTPConfig{
				URL:            "http://example.com",
				Method:         "GET",
				ExpectedStatus: 200,
			},
			wantErr: false,
		},
		{
			name: "valid https config",
			config: &HTTPConfig{
				URL:            "https://example.com",
				Method:         "POST",
				ExpectedStatus: 201,
			},
			wantErr: false,
		},
		{
			name: "valid config with headers",
			config: &HTTPConfig{
				URL:    "https://example.com",
				Method: "GET",
				Headers: map[string]string{
					"Authorization": "Bearer token",
					"Content-Type":  "application/json",
				},
			},
			wantErr: false,
		},
		{
			name: "valid config minimal",
			config: &HTTPConfig{
				URL: "http://example.com",
			},
			wantErr: false,
		},
		{
			name: "missing url",
			config: &HTTPConfig{
				Method: "GET",
			},
			wantErr: true,
		},
		{
			name: "invalid url scheme",
			config: &HTTPConfig{
				URL: "ftp://example.com",
			},
			wantErr: true,
		},
		{
			name: "invalid http method",
			config: &HTTPConfig{
				URL:    "http://example.com",
				Method: "INVALID",
			},
			wantErr: true,
		},
		{
			name: "invalid expected status too low",
			config: &HTTPConfig{
				URL:            "http://example.com",
				ExpectedStatus: 99,
			},
			wantErr: true,
		},
		{
			name: "invalid expected status too high",
			config: &HTTPConfig{
				URL:            "http://example.com",
				ExpectedStatus: 600,
			},
			wantErr: true,
		},
		{
			name: "case insensitive method",
			config: &HTTPConfig{
				URL:    "http://example.com",
				Method: "get",
			},
			wantErr: false,
		},
		{
			name:    "wrong config type",
			config:  nil,
			wantErr: true,
		},
		{
			name: "valid body pattern",
			config: &HTTPConfig{
				URL:         "http://example.com",
				BodyPattern: "status: \\w+",
			},
			wantErr: false,
		},
		{
			name: "valid body pattern reject",
			config: &HTTPConfig{
				URL:               "http://example.com",
				BodyPatternReject: "(error|fail)",
			},
			wantErr: false,
		},
		{
			name: "valid headers pattern",
			config: &HTTPConfig{
				URL: "http://example.com",
				HeadersPattern: map[string]string{
					"Content-Type": "application/json",
					"X-Version":    "v\\d+",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid body pattern regex",
			config: &HTTPConfig{
				URL:         "http://example.com",
				BodyPattern: "[invalid(regex",
			},
			wantErr: true,
		},
		{
			name: "invalid body pattern reject regex",
			config: &HTTPConfig{
				URL:               "http://example.com",
				BodyPatternReject: "[invalid(regex",
			},
			wantErr: true,
		},
		{
			name: "invalid headers pattern regex",
			config: &HTTPConfig{
				URL: "http://example.com",
				HeadersPattern: map[string]string{
					"Content-Type": "[invalid(regex",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var configMap map[string]any
			if tt.config != nil {
				configMap = tt.config.GetConfig()
			}
			spec := &checkerdef.CheckSpec{
				Config: configMap,
			}
			err := checker.Validate(spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("HTTPChecker.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestHTTPChecker_Execute(t *testing.T) {
	t.Parallel()

	// Ensure UserAgent is set (normally done at startup)
	if version.UserAgent == "" {
		version.UserAgent = version.DefaultUserAgent()
	}

	tests := []struct {
		name           string
		config         *HTTPConfig
		serverHandler  http.HandlerFunc
		wantStatus     checkerdef.Status
		checkOutput    func(t *testing.T, output map[string]any)
		contextTimeout time.Duration
	}{
		{
			name: "successful GET request",
			config: &HTTPConfig{
				Method:         "GET",
				ExpectedStatus: 200,
			},
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("Expected GET request, got %s", r.Method)
				}
				w.WriteHeader(http.StatusOK)
			},
			wantStatus: checkerdef.StatusUp,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if statusCode, ok := output["status_code"].(int); !ok || statusCode != 200 {
					t.Errorf("Expected status_code 200, got %v", output["status_code"])
				}
			},
		},
		{
			name: "successful POST request",
			config: &HTTPConfig{
				Method:         "POST",
				ExpectedStatus: 201,
				Body:           `{"test": "data"}`,
			},
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					t.Errorf("Expected POST request, got %s", r.Method)
				}
				w.WriteHeader(http.StatusCreated)
			},
			wantStatus: checkerdef.StatusUp,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if statusCode, ok := output["status_code"].(int); !ok || statusCode != 201 {
					t.Errorf("Expected status_code 201, got %v", output["status_code"])
				}
			},
		},
		{
			name: "unexpected status code",
			config: &HTTPConfig{
				Method:         "GET",
				ExpectedStatus: 200,
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if statusCode, ok := output["status_code"].(int); !ok || statusCode != 404 {
					t.Errorf("Expected status_code 404, got %v", output["status_code"])
				}
			},
		},
		{
			name:   "default method and status",
			config: &HTTPConfig{
				// No method or expected status specified
			},
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("Expected default GET request, got %s", r.Method)
				}
				w.WriteHeader(http.StatusOK)
			},
			wantStatus: checkerdef.StatusUp,
		},
		{
			name: "custom headers",
			config: &HTTPConfig{
				Method: "GET",
				Headers: map[string]string{
					"X-Custom-Header": "test-value",
					"Authorization":   "Bearer token123",
				},
			},
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Custom-Header") != "test-value" {
					t.Errorf("Expected X-Custom-Header: test-value, got %s", r.Header.Get("X-Custom-Header"))
				}
				if r.Header.Get("Authorization") != "Bearer token123" {
					t.Errorf("Expected Authorization: Bearer token123, got %s", r.Header.Get("Authorization"))
				}
				w.WriteHeader(http.StatusOK)
			},
			wantStatus: checkerdef.StatusUp,
		},
		{
			name: "timeout",
			config: &HTTPConfig{
				Method: "GET",
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				// Simulate slow response
				time.Sleep(200 * time.Millisecond)
				w.WriteHeader(http.StatusOK)
			},
			contextTimeout: 50 * time.Millisecond,
			wantStatus:     checkerdef.StatusTimeout,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if errMsg, ok := output["error"].(string); !ok || errMsg != "request timed out" {
					t.Errorf("Expected timeout error, got %v", output["error"])
				}
			},
		},
		{
			name: "default user-agent header",
			config: &HTTPConfig{
				Method: "GET",
			},
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				expectedUA := version.UserAgent
				actualUA := r.Header.Get("User-Agent")
				if actualUA != expectedUA {
					t.Errorf("Expected User-Agent: %s, got %s", expectedUA, actualUA)
				}
				w.WriteHeader(http.StatusOK)
			},
			wantStatus: checkerdef.StatusUp,
		},
		{
			name: "custom user-agent overrides default",
			config: &HTTPConfig{
				Method: "GET",
				Headers: map[string]string{
					"User-Agent": "CustomAgent/1.0",
				},
			},
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				expectedUA := "CustomAgent/1.0"
				actualUA := r.Header.Get("User-Agent")
				if actualUA != expectedUA {
					t.Errorf("Expected User-Agent: %s, got %s", expectedUA, actualUA)
				}
				w.WriteHeader(http.StatusOK)
			},
			wantStatus: checkerdef.StatusUp,
		},
		{
			name: "body_expect found",
			config: &HTTPConfig{
				Method:     "GET",
				BodyExpect: "success",
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("status: success"))
			},
			wantStatus: checkerdef.StatusUp,
		},
		{
			name: "body_expect not found",
			config: &HTTPConfig{
				Method:     "GET",
				BodyExpect: "success",
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("status: failure"))
			},
			wantStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if errMsg, ok := output["error"].(string); !ok || !strings.Contains(errMsg, "not found") {
					t.Errorf("Expected 'not found' error, got %v", output["error"])
				}
			},
		},
		{
			name: "body_reject not found",
			config: &HTTPConfig{
				Method:     "GET",
				BodyReject: "error",
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("status: success"))
			},
			wantStatus: checkerdef.StatusUp,
		},
		{
			name: "body_reject found",
			config: &HTTPConfig{
				Method:     "GET",
				BodyReject: "error",
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("error occurred"))
			},
			wantStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if errMsg, ok := output["error"].(string); !ok || !strings.Contains(errMsg, "found") {
					t.Errorf("Expected 'found' error, got %v", output["error"])
				}
			},
		},
		{
			name: "body_pattern matches",
			config: &HTTPConfig{
				Method:      "GET",
				BodyPattern: "status: \\w+",
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("status: running"))
			},
			wantStatus: checkerdef.StatusUp,
		},
		{
			name: "body_pattern does not match",
			config: &HTTPConfig{
				Method:      "GET",
				BodyPattern: "version: \\d+",
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("status: running"))
			},
			wantStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if errMsg, ok := output["error"].(string); !ok || !strings.Contains(errMsg, "not found") {
					t.Errorf("Expected 'not found' error, got %v", output["error"])
				}
			},
		},
		{
			name: "body_pattern_reject does not match",
			config: &HTTPConfig{
				Method:            "GET",
				BodyPatternReject: "(error|fail|exception)",
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("status: success"))
			},
			wantStatus: checkerdef.StatusUp,
		},
		{
			name: "body_pattern_reject matches",
			config: &HTTPConfig{
				Method:            "GET",
				BodyPatternReject: "(error|fail|exception)",
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("An error occurred"))
			},
			wantStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if errMsg, ok := output["error"].(string); !ok || !strings.Contains(errMsg, "found") {
					t.Errorf("Expected 'found' error, got %v", output["error"])
				}
			},
		},
		{
			name: "headers_pattern matches",
			config: &HTTPConfig{
				Method: "GET",
				HeadersPattern: map[string]string{
					"Content-Type": "application/json",
					"X-Version":    "v\\d+",
				},
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Version", "v2")
				w.WriteHeader(http.StatusOK)
			},
			wantStatus: checkerdef.StatusUp,
		},
		{
			name: "headers_pattern header missing",
			config: &HTTPConfig{
				Method: "GET",
				HeadersPattern: map[string]string{
					"X-Required": ".*",
				},
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			wantStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if errMsg, ok := output["error"].(string); !ok || !strings.Contains(errMsg, "not found") {
					t.Errorf("Expected 'not found' error, got %v", output["error"])
				}
			},
		},
		{
			name: "headers_pattern does not match",
			config: &HTTPConfig{
				Method: "GET",
				HeadersPattern: map[string]string{
					"Content-Type": "application/json",
				},
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
			},
			wantStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if errMsg, ok := output["error"].(string); !ok || !strings.Contains(errMsg, "does not match") {
					t.Errorf("Expected 'does not match' error, got %v", output["error"])
				}
			},
		},
		{
			name: "combined pattern matching",
			config: &HTTPConfig{
				Method:            "GET",
				BodyExpect:        "OK",
				BodyPatternReject: "(error|fail)",
				HeadersPattern: map[string]string{
					"Content-Type": "application/json",
				},
			},
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"OK"}`))
			},
			wantStatus: checkerdef.StatusUp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create test server
			server := httptest.NewServer(tt.serverHandler)
			defer server.Close()

			// Set URL in config
			if tt.config.URL == "" {
				tt.config.URL = server.URL
			}

			// Create context
			ctx := context.Background()
			if tt.contextTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.contextTimeout)
				defer cancel()
			}

			// Execute check
			checker := &HTTPChecker{}
			result, err := checker.Execute(ctx, tt.config)
			if err != nil {
				if tt.wantStatus != checkerdef.StatusError {
					t.Errorf("HTTPChecker.Execute() unexpected error = %v", err)
				}
				if result != nil {
					t.Errorf("HTTPChecker.Execute() result should be nil when error is returned")
				}
				return
			}
			require.NotNil(t, result, "HTTPChecker.Execute() returned nil result without error")

			// Verify status
			if result.Status != tt.wantStatus {
				t.Errorf("HTTPChecker.Execute() status = %v, want %v", result.Status, tt.wantStatus)
			}

			// Verify duration is set
			if result.Duration <= 0 && tt.wantStatus != checkerdef.StatusError {
				t.Errorf("HTTPChecker.Execute() duration should be positive, got %v", result.Duration)
			}

			// Verify output
			if tt.checkOutput != nil {
				tt.checkOutput(t, result.Output)
			}

			// HTTP results no longer duplicate duration into metrics (storage
			// trim, spec 2026-07-24-02): the response time lives in the dedicated
			// Duration field, so Metrics is left nil on every path.
			if result.Status == checkerdef.StatusUp || result.Status == checkerdef.StatusDown {
				require.Nil(t, result.Metrics, "HTTP checker must not populate metrics (duration lives in Duration)")
			}
		})
	}
}

func TestMatchStatusCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		actual   int
		patterns []string
		want     bool
	}{
		{
			name:     "exact match 200",
			actual:   200,
			patterns: []string{"200"},
			want:     true,
		},
		{
			name:     "exact match 201",
			actual:   201,
			patterns: []string{"201"},
			want:     true,
		},
		{
			name:     "exact match fails",
			actual:   404,
			patterns: []string{"200"},
			want:     false,
		},
		{
			name:     "wildcard 2XX matches 200",
			actual:   200,
			patterns: []string{"2XX"},
			want:     true,
		},
		{
			name:     "wildcard 2XX matches 201",
			actual:   201,
			patterns: []string{"2XX"},
			want:     true,
		},
		{
			name:     "wildcard 2XX matches 299",
			actual:   299,
			patterns: []string{"2XX"},
			want:     true,
		},
		{
			name:     "wildcard 2XX does not match 300",
			actual:   300,
			patterns: []string{"2XX"},
			want:     false,
		},
		{
			name:     "wildcard 3XX matches 301",
			actual:   301,
			patterns: []string{"3XX"},
			want:     true,
		},
		{
			name:     "wildcard 4XX matches 404",
			actual:   404,
			patterns: []string{"4XX"},
			want:     true,
		},
		{
			name:     "wildcard 5XX matches 500",
			actual:   500,
			patterns: []string{"5XX"},
			want:     true,
		},
		{
			name:     "multiple patterns first matches",
			actual:   200,
			patterns: []string{"200", "201"},
			want:     true,
		},
		{
			name:     "multiple patterns second matches",
			actual:   201,
			patterns: []string{"200", "201"},
			want:     true,
		},
		{
			name:     "multiple patterns none match",
			actual:   404,
			patterns: []string{"200", "201"},
			want:     false,
		},
		{
			name:     "mixed exact and wildcard",
			actual:   301,
			patterns: []string{"200", "3XX"},
			want:     true,
		},
		{
			name:     "case insensitive wildcard",
			actual:   200,
			patterns: []string{"2xx"},
			want:     true,
		},
		{
			name:     "empty patterns",
			actual:   200,
			patterns: []string{},
			want:     false,
		},
		{
			name:     "wildcard 1XX matches 100",
			actual:   100,
			patterns: []string{"1XX"},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := MatchStatusCode(tt.actual, tt.patterns)
			if got != tt.want {
				t.Errorf("MatchStatusCode(%d, %v) = %v, want %v", tt.actual, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestHTTPConfig_ExpectedStatusCodes_FromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   map[string]any
		want    []string
		wantErr bool
	}{
		{
			name: "single pattern as []string",
			input: map[string]any{
				"url":                   "http://example.com",
				"expected_status_codes": []string{"200"},
			},
			want:    []string{"200"},
			wantErr: false,
		},
		{
			name: "multiple patterns as []string",
			input: map[string]any{
				"url":                   "http://example.com",
				"expected_status_codes": []string{"2XX", "3XX"},
			},
			want:    []string{"2XX", "3XX"},
			wantErr: false,
		},
		{
			name: "patterns as []any",
			input: map[string]any{
				"url":                   "http://example.com",
				"expected_status_codes": []any{"200", "201", "2XX"},
			},
			want:    []string{"200", "201", "2XX"},
			wantErr: false,
		},
		{
			name: "invalid element type",
			input: map[string]any{
				"url":                   "http://example.com",
				"expected_status_codes": []any{"200", 201},
			},
			wantErr: true,
		},
		{
			name: "invalid type",
			input: map[string]any{
				"url":                   "http://example.com",
				"expected_status_codes": "200",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var cfg HTTPConfig
			err := cfg.FromMap(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("FromMap() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if len(cfg.ExpectedStatusCodes) != len(tt.want) {
				t.Errorf("ExpectedStatusCodes length = %d, want %d", len(cfg.ExpectedStatusCodes), len(tt.want))
				return
			}
			for i, v := range tt.want {
				if cfg.ExpectedStatusCodes[i] != v {
					t.Errorf("ExpectedStatusCodes[%d] = %q, want %q", i, cfg.ExpectedStatusCodes[i], v)
				}
			}
		})
	}
}

func TestHTTPConfig_ExpectedStatusCodes_GetConfig(t *testing.T) {
	t.Parallel()

	cfg := HTTPConfig{
		URL:                 "http://example.com",
		ExpectedStatusCodes: []string{"2XX", "3XX"},
	}

	result := cfg.GetConfig()
	codes, ok := result["expectedStatusCodes"].([]string)
	if !ok {
		t.Fatalf("expectedStatusCodes type = %T, want []string", result["expectedStatusCodes"])
	}
	if len(codes) != 2 || codes[0] != "2XX" || codes[1] != "3XX" {
		t.Errorf("expectedStatusCodes = %v, want [2XX 3XX]", codes)
	}
}

func TestHTTPChecker_Validate_ExpectedStatusCodes(t *testing.T) {
	t.Parallel()
	checker := &HTTPChecker{}

	tests := []struct {
		name    string
		codes   []string
		wantErr bool
	}{
		{
			name:    "valid single exact code",
			codes:   []string{"200"},
			wantErr: false,
		},
		{
			name:    "valid single wildcard",
			codes:   []string{"2XX"},
			wantErr: false,
		},
		{
			name:    "valid multiple codes",
			codes:   []string{"200", "201", "3XX"},
			wantErr: false,
		},
		{
			name:    "invalid code too low",
			codes:   []string{"99"},
			wantErr: true,
		},
		{
			name:    "invalid code too high",
			codes:   []string{"600"},
			wantErr: true,
		},
		{
			name:    "invalid wildcard prefix",
			codes:   []string{"6XX"},
			wantErr: true,
		},
		{
			name:    "invalid pattern",
			codes:   []string{"abc"},
			wantErr: true,
		},
		{
			name:    "empty pattern in array",
			codes:   []string{"200", ""},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := &HTTPConfig{
				URL:                 "http://example.com",
				ExpectedStatusCodes: tt.codes,
			}
			spec := &checkerdef.CheckSpec{
				Config: config.GetConfig(),
			}
			err := checker.Validate(spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestHTTPChecker_Execute_ExpectedStatusCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		statusCodes  []string
		serverStatus int
		wantStatus   checkerdef.Status
	}{
		{
			name:         "2XX wildcard accepts 200",
			statusCodes:  []string{"2XX"},
			serverStatus: 200,
			wantStatus:   checkerdef.StatusUp,
		},
		{
			name:         "2XX wildcard accepts 201",
			statusCodes:  []string{"2XX"},
			serverStatus: 201,
			wantStatus:   checkerdef.StatusUp,
		},
		{
			name:         "2XX wildcard rejects 404",
			statusCodes:  []string{"2XX"},
			serverStatus: 404,
			wantStatus:   checkerdef.StatusDown,
		},
		{
			name:         "multiple patterns accepts matching",
			statusCodes:  []string{"200", "201"},
			serverStatus: 201,
			wantStatus:   checkerdef.StatusUp,
		},
		{
			name:         "mixed exact and wildcard",
			statusCodes:  []string{"200", "3XX"},
			serverStatus: 301,
			wantStatus:   checkerdef.StatusUp,
		},
		{
			name:         "redirect codes accepted",
			statusCodes:  []string{"2XX", "3XX"},
			serverStatus: 302,
			wantStatus:   checkerdef.StatusUp,
		},
		{
			name:         "5XX wildcard for error pages",
			statusCodes:  []string{"5XX"},
			serverStatus: 500,
			wantStatus:   checkerdef.StatusUp,
		},
		{
			name:         "4XX for expected errors",
			statusCodes:  []string{"404"},
			serverStatus: 404,
			wantStatus:   checkerdef.StatusUp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.serverStatus)
			}))
			defer server.Close()

			config := &HTTPConfig{
				URL:                 server.URL,
				ExpectedStatusCodes: tt.statusCodes,
			}

			checker := &HTTPChecker{}

			// Retry a few times to absorb rare transient local-connection
			// failures under heavy parallel test load: the httptest server
			// always responds, so a transient dial error must not be read as a
			// status mismatch. A genuinely wrong status is consistent across
			// attempts and still fails the assertion below.
			result, err := checker.Execute(context.Background(), config)
			for attempt := 1; attempt < 3 && err == nil && result.Status != tt.wantStatus; attempt++ {
				time.Sleep(50 * time.Millisecond)
				result, err = checker.Execute(context.Background(), config)
			}
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if result.Status != tt.wantStatus {
				t.Errorf("Execute() status = %v, want %v", result.Status, tt.wantStatus)
			}
		})
	}
}

func TestHTTPChecker_Execute_SecretHeaderSent(t *testing.T) {
	t.Parallel()

	// Ensure UserAgent is set
	if version.UserAgent == "" {
		version.UserAgent = version.DefaultUserAgent()
	}

	r := require.New(t)
	var receivedAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		receivedAPIKey = req.Header.Get("X-Api-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &HTTPConfig{
		URL:           server.URL,
		SecretHeaders: map[string]string{"X-Api-Key": "sk-secret"},
	}
	checker := &HTTPChecker{}
	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)
	r.Equal("sk-secret", receivedAPIKey)
}

// TestHTTPChecker_Execute_BasicAuth covers what actually reaches the wire: the
// folded `basicAuth` credential, the permanent legacy `username`/`password`
// fallback that lazy migration depends on, and the precedence between them and
// an explicit Authorization secret header.
func TestHTTPChecker_Execute_BasicAuth(t *testing.T) {
	t.Parallel()

	// Ensure UserAgent is set
	if version.UserAgent == "" {
		version.UserAgent = version.DefaultUserAgent()
	}

	tests := []struct {
		name         string
		cfg          HTTPConfig
		wantUser     string
		wantPass     string
		wantBasicSet bool
		wantAuthHdr  string
	}{
		{
			name:     "folded basicAuth is sent",
			cfg:      HTTPConfig{BasicAuth: "alice:hunter2"},
			wantUser: "alice", wantPass: "hunter2", wantBasicSet: true,
		},
		{
			name:     "legacy username/password is still sent",
			cfg:      HTTPConfig{Username: "bob", Password: "pw"},
			wantUser: "bob", wantPass: "pw", wantBasicSet: true,
		},
		{
			name:     "folded basicAuth wins over the legacy pair",
			cfg:      HTTPConfig{BasicAuth: "alice:hunter2", Username: "bob", Password: "pw"},
			wantUser: "alice", wantPass: "hunter2", wantBasicSet: true,
		},
		{
			name: "no credential sends no auth",
			cfg:  HTTPConfig{},
		},
		{
			name: "a password without a username sends no auth",
			cfg:  HTTPConfig{Password: "pw"},
		},
		{
			name: "an Authorization secret header wins over the folded credential",
			cfg: HTTPConfig{
				BasicAuth:     "alice:hunter2",
				SecretHeaders: map[string]string{"Authorization": "Bearer secret"},
			},
			wantAuthHdr: "Bearer secret",
		},
		{
			name: "an Authorization secret header wins over the legacy pair",
			cfg: HTTPConfig{
				Username:      "bob",
				Password:      "pw",
				SecretHeaders: map[string]string{"Authorization": "Bearer secret"},
			},
			wantAuthHdr: "Bearer secret",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			var (
				gotUser, gotPass string
				gotBasicSet      bool
				gotAuthHdr       string
			)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				gotUser, gotPass, gotBasicSet = req.BasicAuth()
				gotAuthHdr = req.Header.Get("Authorization")
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			cfg := tc.cfg
			cfg.URL = server.URL

			result, err := (&HTTPChecker{}).Execute(context.Background(), &cfg)
			r.NoError(err)
			r.Equal(checkerdef.StatusUp, result.Status)

			if tc.wantAuthHdr != "" {
				r.Equal(tc.wantAuthHdr, gotAuthHdr)

				return
			}

			r.Equal(tc.wantBasicSet, gotBasicSet)
			r.Equal(tc.wantUser, gotUser)
			r.Equal(tc.wantPass, gotPass)
		})
	}
}

func TestHTTPChecker_Execute_SecretHeaderWinsOnConflict(t *testing.T) {
	t.Parallel()

	// Ensure UserAgent is set
	if version.UserAgent == "" {
		version.UserAgent = version.DefaultUserAgent()
	}

	r := require.New(t)
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		receivedAuth = req.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &HTTPConfig{
		URL:           server.URL,
		Headers:       map[string]string{"Authorization": "Bearer plain"},
		SecretHeaders: map[string]string{"Authorization": "Bearer secret"},
	}
	checker := &HTTPChecker{}
	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)
	r.Equal("Bearer secret", receivedAuth)
}

// TestHTTPChecker_Execute_VerifySsl is the positive + negative control pair:
// a self-signed TLS server fails the check by default (verification on) and
// succeeds when verifySsl: false is set. It also asserts the
// tls_verify_skipped output marker only appears in the latter case.
func TestHTTPChecker_Execute_VerifySsl(t *testing.T) {
	t.Parallel()

	if version.UserAgent == "" {
		version.UserAgent = version.DefaultUserAgent()
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// t.Cleanup (not defer): subtests below are t.Parallel(), so they pause and
	// let this function return immediately; a plain defer would close the
	// server before they actually run. t.Cleanup runs only after every
	// (including parallel) subtest has finished.
	t.Cleanup(server.Close)

	checker := &HTTPChecker{}

	t.Run("default verifies and fails against a self-signed cert", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		result, err := checker.Execute(context.Background(), &HTTPConfig{URL: server.URL})
		r.NoError(err)
		r.Equal(checkerdef.StatusDown, result.Status)
		r.NotContains(result.Output, checkerdef.OutputKeyTLSVerifySkipped)
	})

	t.Run("verifySsl false skips verification and succeeds", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		skip := false
		result, err := checker.Execute(context.Background(), &HTTPConfig{URL: server.URL, VerifySsl: &skip})
		r.NoError(err)
		r.Equal(checkerdef.StatusUp, result.Status)
		r.Equal(true, result.Output[checkerdef.OutputKeyTLSVerifySkipped])
	})

	t.Run("verifySsl true behaves like the default", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		verify := true
		result, err := checker.Execute(context.Background(), &HTTPConfig{URL: server.URL, VerifySsl: &verify})
		r.NoError(err)
		r.Equal(checkerdef.StatusDown, result.Status)
	})
}

// newRedirectingServers builds a fresh final destination + 301-redirector
// pair, plus a pointer that flips true when the final destination is hit.
func newRedirectingServers(t *testing.T) (*httptest.Server, *httptest.Server, *bool) {
	t.Helper()

	hit := false

	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(final.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, final.URL, http.StatusMovedPermanently)
	}))
	t.Cleanup(redirector.Close)

	return final, redirector, &hit
}

// TestHTTPChecker_Execute_FollowRedirects covers both directions: by default
// a redirect is followed to the final destination, and with
// followRedirects: false the check sees and asserts against the first (301)
// response instead.
func TestHTTPChecker_Execute_FollowRedirects(t *testing.T) {
	t.Parallel()

	if version.UserAgent == "" {
		version.UserAgent = version.DefaultUserAgent()
	}

	t.Run("redirects are followed by default", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		_, redirector, finalHit := newRedirectingServers(t)

		result, err := (&HTTPChecker{}).Execute(context.Background(), &HTTPConfig{URL: redirector.URL})
		r.NoError(err)
		r.Equal(checkerdef.StatusUp, result.Status)
		r.Equal(http.StatusOK, result.Output[checkerdef.OutputKeyStatusCode])
		r.True(*finalHit, "the final destination must have been reached")
	})

	t.Run("followRedirects false surfaces the first response", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		final, redirector, finalHit := newRedirectingServers(t)

		noFollow := false
		result, err := (&HTTPChecker{}).Execute(context.Background(), &HTTPConfig{
			URL:             redirector.URL,
			FollowRedirects: &noFollow,
			ExpectedStatus:  http.StatusMovedPermanently,
			HeadersPattern:  map[string]string{"Location": regexp.QuoteMeta(final.URL)},
		})
		r.NoError(err)
		r.Equal(checkerdef.StatusUp, result.Status)
		r.Equal(http.StatusMovedPermanently, result.Output[checkerdef.OutputKeyStatusCode])
		r.False(*finalHit, "the redirect target must not have been reached")
	})
}

// jsonPathStatusIsOK builds the assertion used across the read-gate tests:
// `$.status == "ok"`. Returned fresh each time so no subtest can share (and
// mutate) another's node.
func jsonPathStatusIsOK() *AssertionNode {
	return &AssertionNode{
		Type:     NodeTypeAssertion,
		Path:     "$.status",
		Operator: "eq",
		Value:    "ok",
	}
}

// newJSONBodyServer serves body with a 200 and a JSON content type.
func newJSONBodyServer(t *testing.T, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentTypeJSON)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server
}

// TestHTTPChecker_Execute_JSONPathAssertionsWithoutBodyMatchers pins the
// response-body read gate for checks whose ONLY body-dependent configuration
// is `json_path_assertions`.
//
// Until spec 2026-08-20-04, `bodyDrivesAssertions` (checker.go) listed the
// four `body_*` keys and not JSONPathAssertions, so such a check never read
// the body, `respBody` stayed "", and the assertion block — guarded by
// `respBody != ""` — was skipped in silence: the check reported UP whatever
// the endpoint returned. The first two cases below are exactly that scenario
// and now expect DOWN; the rest are controls that the fix must not move.
func TestHTTPChecker_Execute_JSONPathAssertionsWithoutBodyMatchers(t *testing.T) {
	t.Parallel()

	const (
		bodyDegraded = `{"status":"degraded","detail":"acme is unwell"}`
		bodyOK       = `{"status":"ok"}`
		bodyNotJSON  = `acme is unwell, and this is not JSON`
	)

	tests := []struct {
		name string
		body string
		// mutate applies the assertion configuration under test.
		mutate func(cfg *HTTPConfig)
		// wantStatus is the verdict the checker reports.
		wantStatus checkerdef.Status
		// wantError, when set, must appear in the failure output.
		wantError string
		// wantAssertionDetail requires the failed assertion tree in the
		// output, so an operator can see WHICH assertion failed.
		wantAssertionDetail bool
	}{
		{
			// The headline case: assertions are configured and the response
			// violates them, with no `body_*` key anywhere in the config.
			// This reported UP before the fix.
			name:                "assertions alone, response violates them",
			body:                bodyDegraded,
			mutate:              func(cfg *HTTPConfig) { cfg.JSONPathAssertions = jsonPathStatusIsOK() },
			wantStatus:          checkerdef.StatusDown,
			wantError:           "JSON assertion failed",
			wantAssertionDetail: true,
		},
		{
			// Positive control: the same shape of config against a response
			// that SATISFIES the assertions must stay UP after the fix, so a
			// green result proves the assertions passed and not merely that
			// they were skipped.
			name:       "assertions alone, response satisfies them",
			body:       bodyOK,
			mutate:     func(cfg *HTTPConfig) { cfg.JSONPathAssertions = jsonPathStatusIsOK() },
			wantStatus: checkerdef.StatusUp,
		},
		{
			// A JSON check pointed at a non-JSON endpoint is a real
			// misconfiguration the assertions exist to surface.
			name:       "assertions alone, response is not JSON",
			body:       bodyNotJSON,
			mutate:     func(cfg *HTTPConfig) { cfg.JSONPathAssertions = jsonPathStatusIsOK() },
			wantStatus: checkerdef.StatusDown,
			wantError:  "response body is not valid JSON for assertion evaluation",
		},
		{
			// Control: a `body_*` key already opens the read gate, so these
			// two cases must behave identically before and after the fix.
			name: "assertions with body_expect, response violates the assertions",
			body: bodyDegraded,
			mutate: func(cfg *HTTPConfig) {
				cfg.BodyExpect = "status"
				cfg.JSONPathAssertions = jsonPathStatusIsOK()
			},
			wantStatus:          checkerdef.StatusDown,
			wantError:           "JSON assertion failed",
			wantAssertionDetail: true,
		},
		{
			name: "assertions with body_expect, response satisfies the assertions",
			body: bodyOK,
			mutate: func(cfg *HTTPConfig) {
				cfg.BodyExpect = "status"
				cfg.JSONPathAssertions = jsonPathStatusIsOK()
			},
			wantStatus: checkerdef.StatusUp,
		},
		{
			// KNOWN RESIDUAL, deliberately not fixed by spec 2026-08-20-04:
			// the assertion block is also guarded by `respBody != ""`, so an
			// empty body still skips the assertions in silence. Widening that
			// guard would move the verdict for a second population the
			// blast-radius decision never covered. Pinned so the next reader
			// knows it is a deferral, not an oversight.
			name:       "assertions alone, empty body (known residual)",
			body:       "",
			mutate:     func(cfg *HTTPConfig) { cfg.JSONPathAssertions = jsonPathStatusIsOK() },
			wantStatus: checkerdef.StatusUp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			server := newJSONBodyServer(t, tt.body)

			cfg := &HTTPConfig{URL: server.URL}
			tt.mutate(cfg)

			result := runCheck(t, cfg)
			r.Equal(tt.wantStatus, result.Status)

			if tt.wantError != "" {
				r.Contains(result.Output[checkerdef.OutputKeyError], tt.wantError)
			}

			if tt.wantAssertionDetail {
				r.Contains(result.Output, "json_path_assertions",
					"a failed assertion must report which assertion failed")
			} else {
				r.NotContains(result.Output, "json_path_assertions")
			}
		})
	}
}
