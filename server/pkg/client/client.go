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
)

const (
	httpStatusNoContent = 204
)

// globalRequestCounter is shared across all client instances in the same process
// to provide sequential numbering of HTTP requests.
//
//nolint:gochecknoglobals // Intentional global variable to share request counter across all client instances
var globalRequestCounter atomic.Int64

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

	if err := client.recreateClient(); err != nil {
		return nil, err
	}

	return client, nil
}

// recreateClient creates a new underlying client with current token.
func (c *SolidPingClient) recreateClient() error {
	var opts []ClientOption

	// Use the existing logging transport if enabled
	if c.loggingTransport != nil {
		httpClient := &http.Client{
			Transport: c.loggingTransport,
		}
		opts = append(opts, WithHTTPClient(httpClient))
	}

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
