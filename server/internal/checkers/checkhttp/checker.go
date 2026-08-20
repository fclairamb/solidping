// Package checkhttp provides HTTP/HTTPS endpoint monitoring checks.
package checkhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
			spec.Slug = "http-" + strings.ReplaceAll(hostname, ".", "-")
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

	// Validate SecretHeaders names
	for k := range cfg.SecretHeaders {
		if k == "" {
			return checkerdef.NewConfigError("secretHeaders", "header name must not be empty")
		}
	}

	return nil
}

// Execute performs the HTTP check and returns the result.
//
// Unlike every other checker, HTTP never picks an address itself: http.Transport
// dials by name, which is what gives it Go's Happy Eyeballs. So the address
// family is pinned on the transport (see buildTransport) rather than through
// checkerdef.SelectIPAddr, and the family actually used is observed after the
// fact with httptrace — which is a pure observer, so an `ipVersion: auto` check
// still runs on the shared http.DefaultTransport exactly as before.
func (c *HTTPChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	tracker := &connFamilyTracker{}
	ctx = httptrace.WithClientTrace(ctx, tracker.clientTrace())

	result, err := c.executeRequest(ctx, config)
	if err != nil || result == nil {
		return result, err
	}

	if version := tracker.version(); version != checkerdef.IPVersionAuto {
		if result.Output == nil {
			result.Output = make(map[string]any)
		}

		result.Output[checkerdef.OutputKeyIPVersion] = version.String()
	}

	// The resolved peer address is only observable from the trace, so it is
	// stamped onto the capture here rather than inside executeRequest.
	if result.Diagnostics != nil && result.Diagnostics.FailureResponse != nil {
		result.Diagnostics.FailureResponse.RemoteAddr = tracker.remoteAddr()
	}

	return result, nil
}

// executeRequest is the HTTP probe proper.
//
//nolint:funlen,gocognit,cyclop // HTTP checking with pattern matching requires comprehensive validation
func (c *HTTPChecker) executeRequest(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
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

	// Add basic auth if configured (before headers, so explicit Authorization
	// overrides). Prefers the folded `basicAuth` credential, falling back to the
	// legacy `username`/`password` pair.
	if username, password, ok := cfg.BasicAuthCredentials(); ok {
		req.SetBasicAuth(username, password)
	}

	// Add default User-Agent header
	req.Header.Set("User-Agent", version.UserAgent)

	// Add custom headers (can override User-Agent and Authorization)
	for key, value := range cfg.Headers {
		req.Header.Set(key, value)
	}

	// Add secret headers last so they always win over plain headers on conflict
	for key, value := range cfg.SecretHeaders {
		req.Header.Set(key, value)
	}

	// Execute the request
	skipRedirects := cfg.SkipRedirects()
	skipTLSVerify := cfg.SkipTLSVerify()

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			// followRedirects: false stops at the first response, regardless
			// of maxRedirects.
			if skipRedirects {
				return http.ErrUseLastResponse
			}

			// Allow up to maxRedirects redirects
			if len(via) >= maxRedirects {
				return http.ErrUseLastResponse
			}

			return nil
		},
	}

	// Tunneled check: route every connection through the dialer the worker put
	// on the context (an SSH port-forward today). Only the transport's dial step
	// changes — http.Transport still performs its own TLS handshake over the
	// tunneled conn, with ServerName taken from the URL host, so https targets
	// keep verifying exactly as they do untunneled. Redirects, auth, headers:
	// all unchanged. With no dialer on the context, client.Transport stays nil
	// and net/http uses DefaultTransport as before.
	//
	// verifySsl: false composes with the tunnel dialer on the same transport:
	// whichever of DialContext/TLSClientConfig applies gets set on one shared
	// http.Transport, so client.Transport stays nil only when neither is in
	// play (preserving DefaultTransport's connection pooling in the common case).
	dialer := checkerdef.TunnelDialerFrom(ctx)
	client.Transport = buildTransport(dialer, skipTLSVerify, checkerdef.IPVersionFrom(ctx))

	resp, err := client.Do(req)
	duration := time.Since(start)

	if err != nil {
		// Check if context was canceled or timed out
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: duration,
				Output: withTLSVerifySkipped(map[string]any{
					checkerdef.OutputKeyError: "request timed out",
					checkerdef.OutputKeyURL:   cfg.URL,
				}, skipTLSVerify),
			}, nil
		}

		// An address-family failure is cataloged, not a generic dial error:
		// "the host has no AAAA record" and "this worker has no IPv6 egress"
		// must not read the same as "your service is down".
		return &checkerdef.Result{
			Status:   checkerdef.IPVersionFailureStatus(err),
			Duration: duration,
			Output: withTLSVerifySkipped(map[string]any{
				checkerdef.OutputKeyError: err.Error(),
				checkerdef.OutputKeyURL:   cfg.URL,
			}, skipTLSVerify),
		}, nil
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	// bodyDrivesAssertions is the read gate: it must list EVERY config key
	// whose evaluation needs the response body, because respBody below is
	// populated only when it is true, and every body-reading assertion keys
	// off respBody.
	//
	// JSONPathAssertions belongs here and was missing until spec
	// 2026-08-20-04: a check configured with only `json_path_assertions` and
	// no `body_*` key never read the body, so `respBody` stayed empty and the
	// JSONPath block below — guarded by `respBody != ""` — was skipped without
	// an error or a log, reporting UP whatever the endpoint returned. It is a
	// pointer, so the term is a nil check and not a len().
	//
	// Anything added to HTTPConfig that reads the body goes in this list too;
	// forgetting it fails open, silently, exactly as that bug did.
	bodyDrivesAssertions := cfg.BodyExpect != "" || cfg.BodyReject != "" ||
		cfg.BodyPattern != "" || cfg.BodyPatternReject != "" ||
		cfg.JSONPathAssertions != nil

	// The capture needs the same bytes, so they are read once and shared — the
	// whole point of this feature is that the failing response is ALREADY in
	// memory at failure time, so a capture costs no second request and no
	// extra I/O.
	//
	// The read runs before the regex-compile blocks below so that a check whose
	// pattern fails to compile still captures what the probe saw — a
	// misconfigured regex is exactly when you want the evidence.
	var bodyBytes []byte

	if bodyDrivesAssertions || cfg.CaptureFailureResponse {
		// Limit body size to prevent memory issues
		limitedReader := io.LimitReader(resp.Body, maxBodySize)

		var readErr error

		bodyBytes, readErr = io.ReadAll(limitedReader)
		if readErr != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: duration,
				Output: map[string]any{
					checkerdef.OutputKeyError:      fmt.Sprintf("failed to read response body: %v", readErr),
					checkerdef.OutputKeyURL:        cfg.URL,
					checkerdef.OutputKeyStatusCode: resp.StatusCode,
					checkerdef.OutputKeyMethod:     method,
				},
				// A body we could not read is a body we cannot show: the
				// capture degrades to the partial bytes we did get, which is
				// still the most useful evidence available here.
				Diagnostics: buildFailureCapture(cfg, resp, bodyBytes),
			}, nil
		}
	}

	// THE CAPTURE MUST BE OBSERVATIONALLY INERT ON THE VERDICT. respBody is the
	// single value every assertion block keys off (`body_expect`, the regexes,
	// and — via its `respBody != ""` guard — `json_path_assertions`), so it is
	// populated ONLY when bodyDrivesAssertions says so, never merely because
	// the capture asked for the same bytes. The capture reads
	// bodyBytes directly and never touches respBody: enabling
	// `capture_failure_response` therefore cannot make an assertion start (or
	// stop) running, and cannot move a check between UP and DOWN.
	var respBody string

	if bodyDrivesAssertions {
		respBody = string(bodyBytes)
	}

	// failed builds a post-response failure result, attaching the capture when
	// the check opted in. Every failure return below this point goes through
	// it, so no future branch can silently forget the capture.
	failed := func(output map[string]any) *checkerdef.Result {
		return &checkerdef.Result{
			Status:      checkerdef.StatusDown,
			Duration:    duration,
			Output:      output,
			Diagnostics: buildFailureCapture(cfg, resp, bodyBytes),
		}
	}

	// Compile regex patterns if not already compiled
	if cfg.BodyPattern != "" && cfg.bodyPatternRegex == nil {
		regex, err := regexp.Compile(cfg.BodyPattern)
		if err != nil {
			return failed(map[string]any{
				checkerdef.OutputKeyError:      fmt.Sprintf("invalid body_pattern regex: %v", err),
				checkerdef.OutputKeyURL:        cfg.URL,
				checkerdef.OutputKeyStatusCode: resp.StatusCode,
				checkerdef.OutputKeyMethod:     method,
			}), nil
		}
		cfg.bodyPatternRegex = regex
	}

	if cfg.BodyPatternReject != "" && cfg.bodyPatternRejectRegex == nil {
		regex, err := regexp.Compile(cfg.BodyPatternReject)
		if err != nil {
			return failed(map[string]any{
				checkerdef.OutputKeyError:      fmt.Sprintf("invalid body_pattern_reject regex: %v", err),
				checkerdef.OutputKeyURL:        cfg.URL,
				checkerdef.OutputKeyStatusCode: resp.StatusCode,
				checkerdef.OutputKeyMethod:     method,
			}), nil
		}
		cfg.bodyPatternRejectRegex = regex
	}

	if len(cfg.HeadersPattern) > 0 && len(cfg.headersPatternRegex) == 0 {
		cfg.headersPatternRegex = make(map[string]*regexp.Regexp, len(cfg.HeadersPattern))
		for headerName, pattern := range cfg.HeadersPattern {
			regex, err := regexp.Compile(pattern)
			if err != nil {
				return failed(map[string]any{
					checkerdef.OutputKeyError:      fmt.Sprintf("invalid headers_pattern regex for header %q: %v", headerName, err),
					checkerdef.OutputKeyURL:        cfg.URL,
					checkerdef.OutputKeyStatusCode: resp.StatusCode,
					checkerdef.OutputKeyMethod:     method,
				}), nil
			}
			cfg.headersPatternRegex[headerName] = regex
		}
	}

	// Apply body pattern matching
	if cfg.BodyExpect != "" {
		if !strings.Contains(respBody, cfg.BodyExpect) {
			return failed(map[string]any{
				checkerdef.OutputKeyError:      fmt.Sprintf("Expected string %q not found in response body", cfg.BodyExpect),
				checkerdef.OutputKeyURL:        cfg.URL,
				checkerdef.OutputKeyStatusCode: resp.StatusCode,
				checkerdef.OutputKeyMethod:     method,
			}), nil
		}
	}

	if cfg.BodyReject != "" {
		if strings.Contains(respBody, cfg.BodyReject) {
			return failed(map[string]any{
				checkerdef.OutputKeyError:      fmt.Sprintf("Rejected string %q found in response body", cfg.BodyReject),
				checkerdef.OutputKeyURL:        cfg.URL,
				checkerdef.OutputKeyStatusCode: resp.StatusCode,
				checkerdef.OutputKeyMethod:     method,
			}), nil
		}
	}

	if cfg.bodyPatternRegex != nil {
		if !cfg.bodyPatternRegex.MatchString(respBody) {
			return failed(map[string]any{
				checkerdef.OutputKeyError:      fmt.Sprintf("Expected pattern %q not found in response body", cfg.BodyPattern),
				checkerdef.OutputKeyURL:        cfg.URL,
				checkerdef.OutputKeyStatusCode: resp.StatusCode,
				checkerdef.OutputKeyMethod:     method,
			}), nil
		}
	}

	if cfg.bodyPatternRejectRegex != nil {
		if cfg.bodyPatternRejectRegex.MatchString(respBody) {
			return failed(map[string]any{
				checkerdef.OutputKeyError:      fmt.Sprintf("Rejected pattern %q found in response body", cfg.BodyPatternReject),
				checkerdef.OutputKeyURL:        cfg.URL,
				checkerdef.OutputKeyStatusCode: resp.StatusCode,
				checkerdef.OutputKeyMethod:     method,
			}), nil
		}
	}

	// Apply header pattern matching
	if len(cfg.headersPatternRegex) > 0 {
		for headerName, headerRegex := range cfg.headersPatternRegex {
			headerValue := resp.Header.Get(headerName)
			if headerValue == "" {
				return failed(map[string]any{
					checkerdef.OutputKeyError:      fmt.Sprintf("Required header %q not found in response", headerName),
					checkerdef.OutputKeyURL:        cfg.URL,
					checkerdef.OutputKeyStatusCode: resp.StatusCode,
					checkerdef.OutputKeyMethod:     method,
				}), nil
			}
			if !headerRegex.MatchString(headerValue) {
				errMsg := fmt.Sprintf(
					"Header %q value does not match pattern %q",
					headerName, cfg.HeadersPattern[headerName],
				)
				return failed(map[string]any{
					checkerdef.OutputKeyError:      errMsg,
					checkerdef.OutputKeyURL:        cfg.URL,
					checkerdef.OutputKeyStatusCode: resp.StatusCode,
					checkerdef.OutputKeyMethod:     method,
				}), nil
			}
		}
	}

	// Apply JSONPath assertions
	if cfg.JSONPathAssertions != nil && respBody != "" {
		var jsonData any
		if unmarshalErr := json.Unmarshal([]byte(respBody), &jsonData); unmarshalErr != nil {
			return failed(map[string]any{
				checkerdef.OutputKeyError:      "response body is not valid JSON for assertion evaluation",
				checkerdef.OutputKeyURL:        cfg.URL,
				checkerdef.OutputKeyStatusCode: resp.StatusCode,
				checkerdef.OutputKeyMethod:     method,
			}), nil
		}

		assertionResult := cfg.JSONPathAssertions.Evaluate(jsonData)
		if !assertionResult.Pass {
			return failed(map[string]any{
				checkerdef.OutputKeyError:      "JSON assertion failed",
				checkerdef.OutputKeyURL:        cfg.URL,
				checkerdef.OutputKeyStatusCode: resp.StatusCode,
				checkerdef.OutputKeyMethod:     method,
				"json_path_assertions":         assertionResult,
			}), nil
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

	finalResult := &checkerdef.Result{
		Status:   status,
		Duration: duration,
		Output: withTLSVerifySkipped(map[string]any{
			checkerdef.OutputKeyURL:        cfg.URL,
			checkerdef.OutputKeyStatusCode: resp.StatusCode,
			checkerdef.OutputKeyMethod:     method,
		}, skipTLSVerify),
	}

	// The unexpected-status case is the single most common HTTP failure, and
	// the only one that reaches here. A successful check never carries a
	// capture: the whole feature exists to explain failures.
	if status != checkerdef.StatusUp {
		finalResult.Diagnostics = buildFailureCapture(cfg, resp, bodyBytes)
	}

	return finalResult, nil
}

// withTLSVerifySkipped adds the tls_verify_skipped marker to a result output
// map when the check ran with certificate verification disabled, so the
// reduced trust is visible in result details rather than only in config.
func withTLSVerifySkipped(output map[string]any, skipped bool) map[string]any {
	if skipped {
		output[checkerdef.OutputKeyTLSVerifySkipped] = true
	}

	return output
}

// buildTransport delegates to the shared checkerdef helper. It stays here as a
// named seam so this package's tests (and the Execute path) keep reading the
// same way after the helper moved to checkerdef for reuse by checkprometheus.
func buildTransport(
	dialer checkerdef.ContextDialer, skipTLSVerify bool, version checkerdef.IPVersion,
) http.RoundTripper {
	return checkerdef.BuildHTTPTransport(dialer, skipTLSVerify, version)
}

// familyDialContext delegates to checkerdef.FamilyDialContext.
func familyDialContext(version checkerdef.IPVersion) func(context.Context, string, string) (net.Conn, error) {
	return checkerdef.FamilyDialContext(version)
}

// connFamilyTracker observes which address family the request actually
// connected over. It is a pure httptrace observer — it never influences dialing
// — so an `ipVersion: auto` check keeps Go's Happy Eyeballs behavior untouched
// and merely reports the outcome.
type connFamilyTracker struct {
	mu     sync.Mutex
	family checkerdef.IPVersion
	// addr is the peer address the request actually reached, used only to
	// annotate a failure capture ("which of the CDN's edges served me this
	// challenge page?"). Empty when no connection was established.
	addr string
}

// clientTrace returns the httptrace hooks to attach to the request context.
func (t *connFamilyTracker) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Conn == nil {
				return
			}

			remote := info.Conn.RemoteAddr()

			addr, ok := remote.(*net.TCPAddr)
			if !ok || addr.IP == nil {
				return
			}

			t.mu.Lock()
			defer t.mu.Unlock()

			t.family = checkerdef.IPVersionOf(addr.IP)
			t.addr = remote.String()
		},
	}
}

// version reports the observed family, or IPVersionAuto when no connection was
// established (DNS failure, a redirect chain that never dialed, a non-TCP
// transport in a test).
func (t *connFamilyTracker) version() checkerdef.IPVersion {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.family != checkerdef.IPVersionIPv4 && t.family != checkerdef.IPVersionIPv6 {
		return checkerdef.IPVersionAuto
	}

	return t.family
}

// remoteAddr reports the peer address of the last established connection, or
// "" when none was established.
func (t *connFamilyTracker) remoteAddr() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.addr
}
