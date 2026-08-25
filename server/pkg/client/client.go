// Package client provides a wrapper around the generated OpenAPI client for the SolidPing API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

var (
	// ErrAuthenticationFailed is returned when authentication fails.
	ErrAuthenticationFailed = errors.New("authentication failed")
	// ErrUnauthorized is returned when a request is unauthorized.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrUnexpectedStatus is returned when an unexpected HTTP status is received.
	ErrUnexpectedStatus = errors.New("unexpected response status")
	// ErrTokenNotFound is returned when a token is not found.
	ErrTokenNotFound = errors.New("token not found")
	// ErrPasswordChangeRequired is returned when the server refuses a request
	// because the account must rotate its password first (HTTP 403 with code
	// PASSWORD_CHANGE_REQUIRED — spec 2026-08-23-04). Surfaced as its own
	// sentinel so the CLI can print what to do instead of an opaque
	// "unexpected response status: 403".
	ErrPasswordChangeRequired = errors.New(
		"password change required: this account must set a new password before it can be used\n" +
			"  Sign in to the dashboard and complete the \"set a new password\" screen,\n" +
			"  or POST /api/v1/auth/change-password with {\"currentPassword\":\"…\",\"newPassword\":\"…\"}.\n" +
			"  A freshly installed server seeds its admin with a published default password " +
			"and forces this rotation on first login")
)

// passwordChangeRequiredCode is the machine-readable error code the server
// returns while an account carries must_change_password.
const passwordChangeRequiredCode = "PASSWORD_CHANGE_REQUIRED"

const (
	httpStatusNoContent = 204
)

// globalRequestCounter is shared across all client instances in the same process
// to provide sequential numbering of HTTP requests.
//
//nolint:gochecknoglobals // Intentional global variable to share request counter across all client instances
var globalRequestCounter atomic.Int64

// rotationGateRoundTripper turns the server's forced-password-rotation refusal
// into a typed Go error, at the one place every API call in this package passes
// through — the generated OpenAPI methods, the hand-written wrappers and
// rawRequest alike.
//
// Doing it here rather than per call site is what makes the CLI failure
// actionable everywhere at once: without it, `solidping checks list` against a
// flagged account reports "unexpected response status: 403", which reads as a
// permissions problem and sends the operator looking for a role to grant.
type rotationGateRoundTripper struct {
	transport http.RoundTripper
}

// RoundTrip inspects only 403 responses, and restores the body it read so an
// ordinary FORBIDDEN still decodes normally in the caller.
func (r *rotationGateRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.transport.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		return resp, err //nolint:wrapcheck // transparent pass-through of the inner transport's error
	}

	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// A body we could not read is not a rotation refusal we can recognize, and
	// the status is still a legitimate 403 — hand it back with an empty body and
	// let the caller report the status. Swallowing readErr here is deliberate:
	// turning a transport-level read hiccup into a hard error would be a worse
	// diagnostic than the 403 the server actually sent.
	if readErr != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))

		return resp, nil //nolint:nilerr // see above: the 403 is the useful signal, not readErr
	}

	resp.Body = io.NopCloser(bytes.NewReader(body))

	var apiErr struct {
		Code string `json:"code"`
	}

	if json.Unmarshal(body, &apiErr) == nil && apiErr.Code == passwordChangeRequiredCode {
		return nil, ErrPasswordChangeRequired
	}

	return resp, nil
}

// loggingRoundTripper wraps an http.RoundTripper to log requests and responses.
type loggingRoundTripper struct {
	transport http.RoundTripper
}

// RoundTrip implements http.RoundTripper and logs the request/response.
func (l *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Increment global counter for sequential numbering across all clients
	count := globalRequestCounter.Add(1)

	// Log request
	l.logRequest(req, count)

	// Execute the request
	resp, err := l.transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// Log response
	l.logResponse(resp, count)

	return resp, nil
}

// logRequest logs the HTTP request to stderr.
func (l *loggingRoundTripper) logRequest(req *http.Request, count int64) {
	var buf bytes.Buffer

	// Request line
	fmt.Fprintf(&buf, "=== Request %d ===\n", count)
	fmt.Fprintf(&buf, "%s %s HTTP/1.1\n", req.Method, req.URL.RequestURI())
	fmt.Fprintf(&buf, "Host: %s\n", req.URL.Host)

	// Headers
	for name, values := range req.Header {
		for _, value := range values {
			fmt.Fprintf(&buf, "%s: %s\n", name, value)
		}
	}

	// Body
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err == nil {
			// Restore body for actual request
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			if len(bodyBytes) > 0 {
				fmt.Fprintf(&buf, "\n")
				l.formatBody(&buf, req.Header.Get("Content-Type"), bodyBytes)
			}
		}
	}

	fmt.Fprintf(&buf, "=== / Request %d ===\n\n", count)
	fmt.Fprint(os.Stderr, buf.String())
}

// logResponse logs the HTTP response to stderr.
func (l *loggingRoundTripper) logResponse(resp *http.Response, count int64) {
	var buf bytes.Buffer

	// Status line
	fmt.Fprintf(&buf, "=== Response %d ===\n", count)
	fmt.Fprintf(&buf, "HTTP/1.1 %d %s\n", resp.StatusCode, resp.Status)

	// Headers
	for name, values := range resp.Header {
		for _, value := range values {
			fmt.Fprintf(&buf, "%s: %s\n", name, value)
		}
	}

	// Body
	if resp.Body != nil {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err == nil {
			// Restore body for actual consumption
			resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			if len(bodyBytes) > 0 {
				fmt.Fprintf(&buf, "\n")
				l.formatBody(&buf, resp.Header.Get("Content-Type"), bodyBytes)
			}
		}
	}

	fmt.Fprintf(&buf, "=== / Response %d ===\n\n", count)
	fmt.Fprint(os.Stderr, buf.String())
}

// formatBody formats the body, pretty-printing JSON if applicable.
func (l *loggingRoundTripper) formatBody(buf *bytes.Buffer, contentType string, bodyBytes []byte) {
	// Check if content type is JSON
	if strings.Contains(contentType, "application/json") {
		var jsonData any
		if err := json.Unmarshal(bodyBytes, &jsonData); err == nil {
			// Successfully parsed JSON, pretty-print it
			prettyJSON, err := json.MarshalIndent(jsonData, "", "  ")
			if err == nil {
				buf.Write(prettyJSON)
				buf.WriteString("\n")

				return
			}
		}
	}

	// Not JSON or parsing failed, print raw body
	buf.Write(bodyBytes)
	buf.WriteString("\n")
}

// Config holds configuration for the SolidPing client.
type Config struct {
	BaseURL string
	Token   string
	Verbose bool
}

// SolidPingClient wraps the generated OpenAPI client.
type SolidPingClient struct {
	*ClientWithResponses
	config           Config
	token            string
	loggingTransport *loggingRoundTripper
	// transport is what every request in this package actually goes out on:
	// the optional logging tripper, wrapped by the forced-rotation gate. Held
	// on the struct (rather than rebuilt per call site) so the generated
	// client and the three hand-written raw paths cannot drift apart and leave
	// one of them ungated.
	transport http.RoundTripper
}

// New creates a new SolidPing API client.
func New(cfg Config) (*SolidPingClient, error) {
	client := &SolidPingClient{
		config: cfg,
		token:  cfg.Token,
	}

	// Initialize logging transport if enabled (reused across client recreations)
	if shouldLogHTTP(cfg.Verbose) {
		client.loggingTransport = &loggingRoundTripper{
			transport: http.DefaultTransport,
		}
	}

	var inner http.RoundTripper
	if client.loggingTransport != nil {
		inner = client.loggingTransport
	} else {
		inner = http.DefaultTransport
	}

	client.transport = &rotationGateRoundTripper{transport: inner}

	if err := client.recreateClient(); err != nil {
		return nil, err
	}

	return client, nil
}

// httpClient returns the http.Client every request in this package must use.
func (c *SolidPingClient) httpClient() *http.Client {
	if c.transport == nil {
		return http.DefaultClient
	}

	return &http.Client{Transport: c.transport}
}

// recreateClient creates a new underlying client with current token.
func (c *SolidPingClient) recreateClient() error {
	opts := []ClientOption{WithHTTPClient(c.httpClient())}

	if c.token != "" {
		token := c.token // capture for closure

		opts = append(opts, WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		}))
	}

	generatedClient, err := NewClientWithResponses(c.config.BaseURL, opts...)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	c.ClientWithResponses = generatedClient

	return nil
}

// shouldLogHTTP checks if HTTP logging is enabled via verbose flag or environment variable.
func shouldLogHTTP(verbose bool) bool {
	if verbose {
		return true
	}
	val := os.Getenv("SP_LOG_HTTP_CALLS")
	return val == "1" || val == "true"
}

// SetToken sets the authentication token for the client.
func (c *SolidPingClient) SetToken(token string) {
	c.token = token
	_ = c.recreateClient()
}

// Login performs email/password authentication. Org is optional.
func (c *SolidPingClient) Login(ctx context.Context, org string, email, password string) (*LoginResponse, error) {
	body := LoginJSONRequestBody{
		Email:    openapi_types.Email(email),
		Password: password,
	}

	if org != "" {
		body.Org = &org
	}

	resp, err := c.LoginWithResponse(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}

	if resp.JSON200 == nil {
		if resp.JSON401 != nil {
			return nil, fmt.Errorf("%w: %s", ErrAuthenticationFailed, derefString(&resp.JSON401.Title, "Invalid credentials"))
		}

		return nil, fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode())
	}

	// Store the access token for future requests
	if resp.JSON200.AccessToken != nil {
		c.SetToken(*resp.JSON200.AccessToken)
	}

	return resp.JSON200, nil
}

// Me gets the current user information.
func (c *SolidPingClient) Me(ctx context.Context) (*MeResponse, error) {
	resp, err := c.GetCurrentUserWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("get current user failed: %w", err)
	}

	if resp.JSON200 == nil {
		if resp.JSON401 != nil {
			return nil, fmt.Errorf("%w: %s", ErrUnauthorized, derefString(&resp.JSON401.Title, "Authentication required"))
		}

		return nil, fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode())
	}

	return resp.JSON200, nil
}

// SessionRequiresPasswordChange reports whether the account behind the client's
// current token must rotate its password before it can be used.
//
// It reads GET /api/v1/auth/me — deliberately, because that is one of the three
// endpoints a flagged session can still reach, so this probe never trips the
// gate it is asking about. Hand-written against rawRequest rather than the
// generated Me(): pkg/client/client_generated.go is regenerated once per
// batch (wiki/conventions/generated-client.md), so a typed field added to
// openapi.yaml today is not available to compile against today.
func (c *SolidPingClient) SessionRequiresPasswordChange(ctx context.Context) (bool, error) {
	var resp struct {
		User struct {
			MustChangePassword bool `json:"mustChangePassword"`
		} `json:"user"`
	}

	if _, err := c.rawRequest(ctx, http.MethodGet, "/api/v1/auth/me", nil, &resp); err != nil {
		return false, err
	}

	return resp.User.MustChangePassword, nil
}

// Refresh refreshes the access token using a refresh token.
func (c *SolidPingClient) Refresh(ctx context.Context, refreshToken string) (*RefreshResponse, error) {
	body := RefreshTokenJSONRequestBody{
		RefreshToken: refreshToken,
	}

	resp, err := c.RefreshTokenWithResponse(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("refresh token failed: %w", err)
	}

	if resp.JSON200 == nil {
		if resp.JSON401 != nil {
			return nil, fmt.Errorf("%w: %s", ErrAuthenticationFailed, derefString(&resp.JSON401.Title, "Invalid refresh token"))
		}

		return nil, fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode())
	}

	// Store the new access token
	if resp.JSON200.AccessToken != nil {
		c.SetToken(*resp.JSON200.AccessToken)
	}

	return resp.JSON200, nil
}

// Logout logs out the current session.
func (c *SolidPingClient) Logout(ctx context.Context, deleteAllTokens bool) (*LogoutResponse, error) {
	body := LogoutJSONRequestBody{
		DeleteAllTokens: &deleteAllTokens,
	}

	resp, err := c.LogoutWithResponse(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("logout failed: %w", err)
	}

	if resp.JSON200 == nil {
		return nil, fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode())
	}

	// Clear the token after logout
	c.SetToken("")

	return resp.JSON200, nil
}

// CreateToken creates a new Personal Access Token scoped to an org.
func (c *SolidPingClient) CreateToken(
	ctx context.Context,
	org, name string,
	expiresAt *time.Time,
) (*CreateTokenResponse, error) {
	body := CreateTokenJSONRequestBody{
		Name:      name,
		ExpiresAt: expiresAt,
	}

	resp, err := c.CreateTokenWithResponse(ctx, org, body)
	if err != nil {
		return nil, fmt.Errorf("create token failed: %w", err)
	}

	if resp.JSON201 == nil {
		return nil, fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode())
	}

	return resp.JSON201, nil
}

// GetTokens lists all tokens for the current user across all orgs.
func (c *SolidPingClient) GetTokens(ctx context.Context) (*TokenListResponse, error) {
	resp, err := c.ListAllTokensWithResponse(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list tokens failed: %w", err)
	}

	if resp.JSON200 == nil {
		return nil, fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode())
	}

	return resp.JSON200, nil
}

// RevokeToken revokes a token by UID.
func (c *SolidPingClient) RevokeToken(ctx context.Context, tokenUID string) error {
	uid, err := uuid.Parse(tokenUID)
	if err != nil {
		return fmt.Errorf("invalid token UID: %w", err)
	}

	resp, err := c.RevokeTokenWithResponse(ctx, uid)
	if err != nil {
		return fmt.Errorf("revoke token failed: %w", err)
	}

	if resp.StatusCode() != httpStatusNoContent {
		if resp.JSON404 != nil {
			return fmt.Errorf("%w: %s", ErrTokenNotFound, derefString(&resp.JSON404.Title, "Token not found"))
		}

		return fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode())
	}

	return nil
}

// SwitchOrg switches to a different organization.
func (c *SolidPingClient) SwitchOrg(ctx context.Context, orgSlug string) (*LoginResponse, error) {
	body := SwitchOrgJSONRequestBody{
		Org: orgSlug,
	}

	resp, err := c.SwitchOrgWithResponse(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("switch org failed: %w", err)
	}

	if resp.JSON200 == nil {
		if resp.JSON401 != nil {
			return nil, fmt.Errorf("%w: %s", ErrUnauthorized, derefString(&resp.JSON401.Title, "Unauthorized"))
		}

		return nil, fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode())
	}

	// Store the new access token
	if resp.JSON200.AccessToken != nil {
		c.SetToken(*resp.JSON200.AccessToken)
	}

	return resp.JSON200, nil
}

// derefString dereferences a string pointer with a default value.
func derefString(s *string, defaultVal string) string {
	if s != nil {
		return *s
	}

	return defaultVal
}

// rawRequest performs an authenticated HTTP request against the API and decodes
// the JSON response into out (when out != nil and the body is non-empty). It is
// used by hand-written wrappers for endpoints that aren't yet in the generated
// client.
func (c *SolidPingClient) rawRequest(
	ctx context.Context, method, path string, body any, out any,
) (int, error) {
	var bodyReader io.Reader

	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("marshal body: %w", err)
		}

		bodyReader = bytes.NewReader(buf)
	}

	url := strings.TrimRight(c.config.BaseURL, "/") + path

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best effort

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr Error
		if jsonErr := json.Unmarshal(respBody, &apiErr); jsonErr == nil && apiErr.Title != "" {
			return resp.StatusCode, fmt.Errorf("%w: %s", ErrUnexpectedStatus, apiErr.Title)
		}

		return resp.StatusCode, fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode)
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}

	return resp.StatusCode, nil
}

// DependencyRef is a minimal {uid, slug, name} reference used by dependency
// responses.
type DependencyRef struct {
	UID  string `json:"uid"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// DependencyEdge is one row in the per-check dependency response.
type DependencyEdge struct {
	UID         string        `json:"uid"`
	ParentCheck DependencyRef `json:"parentCheck"`
	ChildCheck  DependencyRef `json:"childCheck"`
	Kind        string        `json:"kind"`
	Description *string       `json:"description,omitempty"`
}

// PerCheckDependencies is the data payload for ListCheckDependencies.
type PerCheckDependencies struct {
	DependsOn    []DependencyEdge `json:"dependsOn"`
	DependedOnBy []DependencyEdge `json:"dependedOnBy"`
}

// CreateDependencyBody is the body for AddCheckDependency.
type CreateDependencyBody struct {
	ParentCheckUID string  `json:"parentCheckUid"`
	Kind           string  `json:"kind"`
	Description    *string `json:"description,omitempty"`
}

// ListCheckDependencies returns the parents and children of a check.
func (c *SolidPingClient) ListCheckDependencies(
	ctx context.Context, org, check string,
) (*PerCheckDependencies, error) {
	var resp struct {
		Data PerCheckDependencies `json:"data"`
	}

	if _, err := c.rawRequest(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/orgs/%s/checks/%s/dependencies", org, check), nil, &resp); err != nil {
		return nil, err
	}

	return &resp.Data, nil
}

// AddCheckDependency creates one parent edge for a child check.
func (c *SolidPingClient) AddCheckDependency(
	ctx context.Context, org, child string, body CreateDependencyBody,
) (*DependencyEdge, error) {
	var dep DependencyEdge
	if _, err := c.rawRequest(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/orgs/%s/checks/%s/dependencies", org, child), body, &dep); err != nil {
		return nil, err
	}

	return &dep, nil
}

// DeleteCheckDependency removes one edge by uid.
func (c *SolidPingClient) DeleteCheckDependency(
	ctx context.Context, org, child, depUID string,
) error {
	_, err := c.rawRequest(ctx, http.MethodDelete,
		fmt.Sprintf("/api/v1/orgs/%s/checks/%s/dependencies/%s", org, child, depUID), nil, nil)
	return err
}

// RawPutCheckBySlug PUTs a free-form payload to /checks/{slug}. Used by the
// CLI to apply dependsOn together with the existing check config — the
// generated UpsertCheckRequest doesn't yet expose dependsOn.
func (c *SolidPingClient) RawPutCheckBySlug(
	ctx context.Context, org, slug string, body map[string]any,
) (int, error) {
	return c.rawRequest(ctx, http.MethodPut,
		fmt.Sprintf("/api/v1/orgs/%s/checks/%s", org, slug), body, nil)
}

// rawRequestBytes is rawRequest's sibling for sending a pre-serialized body
// (e.g. a hand-authored YAML manifest) with an explicit Content-Type. The
// decoded response (if any) is written into out.
func (c *SolidPingClient) rawRequestBytes(
	ctx context.Context, method, path, contentType string, body []byte, out any,
) error {
	url := strings.TrimRight(c.config.BaseURL, "/") + path

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best effort

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr Error
		if jsonErr := json.Unmarshal(respBody, &apiErr); jsonErr == nil && apiErr.Title != "" {
			return fmt.Errorf("%w: %s", ErrUnexpectedStatus, apiErr.Title)
		}

		return fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode)
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}

	return nil
}

// HeartbeatOptions carries the optional inputs for a heartbeat ping.
type HeartbeatOptions struct {
	Token   string
	Status  string
	Message string
}

// SendHeartbeat posts a heartbeat ping for the given org + identifier. This is a
// public ingestion route: the per-check heartbeat token (when configured) is
// passed as the `token` query parameter, not as the bearer token.
func (c *SolidPingClient) SendHeartbeat(
	ctx context.Context, org, identifier string, opts HeartbeatOptions,
) error {
	path := fmt.Sprintf("/api/v1/heartbeat/%s/%s", org, identifier)

	query := url.Values{}
	if opts.Token != "" {
		query.Set("token", opts.Token)
	}
	if opts.Status != "" {
		query.Set("status", opts.Status)
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var body any
	if opts.Message != "" {
		body = map[string]string{"message": opts.Message}
	}

	_, err := c.rawRequest(ctx, http.MethodPost, path, body, nil)

	return err
}

// ExportChecks fetches the org's checks as a raw export document (JSON bytes),
// returned verbatim so the CLI can write it to a file unchanged.
func (c *SolidPingClient) ExportChecks(ctx context.Context, org string) (json.RawMessage, error) {
	var doc json.RawMessage
	if _, err := c.rawRequest(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/orgs/%s/checks/export", org), nil, &doc); err != nil {
		return nil, err
	}

	return doc, nil
}

// ImportChecks posts an export document to the import endpoint. dryRun
// previews without mutating. contentType selects the server's parse path
// (JSON or YAML) and is sent verbatim as the request Content-Type; the body
// itself is sent unmodified.
func (c *SolidPingClient) ImportChecks(
	ctx context.Context, org string, body []byte, contentType string, dryRun bool,
) (json.RawMessage, error) {
	path := fmt.Sprintf("/api/v1/orgs/%s/checks/import", org)
	if dryRun {
		path += "?dryRun=true"
	}

	var result json.RawMessage
	if err := c.rawRequestBytes(ctx, http.MethodPost, path, contentType, body, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// ApplyOptions mirrors the apply endpoint's query flags for the CLI.
type ApplyOptions struct {
	DryRun bool
	Prune  bool
	Force  bool
}

// ApplyChecks posts a manifest (JSON or YAML bytes) to the apply endpoint with
// the given options. contentType selects the parse path server-side.
func (c *SolidPingClient) ApplyChecks(
	ctx context.Context, org string, body []byte, contentType string, opts ApplyOptions,
) (json.RawMessage, error) {
	params := make([]string, 0, 3)
	if opts.DryRun {
		params = append(params, "dryRun=true")
	}
	if opts.Prune {
		params = append(params, "prune=true")
	}
	if opts.Force {
		params = append(params, "force=true")
	}

	path := fmt.Sprintf("/api/v1/orgs/%s/checks/apply", org)
	if len(params) > 0 {
		path += "?" + strings.Join(params, "&")
	}

	var result json.RawMessage
	if err := c.rawRequestBytes(ctx, http.MethodPost, path, contentType, body, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// GetOnCallICalFeed fetches the public iCal (text/calendar) feed for an on-call
// schedule by its feed secret. This is an unauthenticated route that returns
// raw VCALENDAR text, not JSON, so it uses a bespoke reader rather than the
// generated client.
func (c *SolidPingClient) GetOnCallICalFeed(ctx context.Context, secret string) (string, error) {
	url := strings.TrimRight(c.config.BaseURL, "/") +
		fmt.Sprintf("/api/v1/on-call-schedules/%s/feed.ics", secret)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best effort

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode)
	}

	return string(respBody), nil
}
