// Package apihelper provides helper functions for API client operations.
package apihelper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/term"

	"github.com/fclairamb/solidping/server/internal/oauth"
	"github.com/fclairamb/solidping/server/pkg/cli/config"
	"github.com/fclairamb/solidping/server/pkg/client"
)

var (
	// ErrNoAuthentication is returned when no authentication method is configured.
	ErrNoAuthentication = errors.New(
		"no authentication configured: please run 'sp auth login' or configure PAT in settings.json")
	// ErrNoAccessToken is returned when no access token is received.
	ErrNoAccessToken = errors.New("no access token received")
	// ErrInvalidToken is returned when a token cannot be parsed.
	ErrInvalidToken = errors.New("invalid token")
	// ErrRefreshTokenInvalid is returned when refresh token is invalid or expired.
	ErrRefreshTokenInvalid = errors.New("refresh token is invalid or expired")
)

// patTokenPrefix marks an opaque Personal Access Token (`pat_…`), as minted by
// POST /orgs/:org/tokens. Used to tell a PAT credential apart from a session JWT.
const patTokenPrefix = "pat_"

// TokenData represents the stored token information.
type TokenData struct {
	AccessToken           string    `json:"accessToken"`
	AccessTokenExpiresAt  time.Time `json:"accessTokenExpiresAt"`
	RefreshToken          string    `json:"refreshToken"`
	RefreshTokenExpiresAt time.Time `json:"refreshTokenExpiresAt"`
	// PAT holds a Personal Access Token saved by the browser-login flow
	// (`sp auth login`). When set, it is used directly as the bearer
	// credential and never expires client-side — the server enforces any
	// expiry. A PAT and a JWT session are mutually exclusive in one file.
	PAT string `json:"pat,omitempty"`
}

// IsAccessTokenValid checks if the access token is still valid.
func (t *TokenData) IsAccessTokenValid() bool {
	return time.Now().Before(t.AccessTokenExpiresAt)
}

// IsRefreshTokenValid checks if the refresh token is still valid.
func (t *TokenData) IsRefreshTokenValid() bool {
	return t.RefreshToken != "" && time.Now().Before(t.RefreshTokenExpiresAt)
}

// parseJWTExpiration extracts the expiration time from a JWT token.
func parseJWTExpiration(tokenString string) (time.Time, error) {
	// Parse without verifying signature (we just need the expiration time)
	token, _, err := jwt.NewParser().ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse JWT: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return time.Time{}, ErrInvalidToken
	}

	exp, ok := claims["exp"]
	if !ok {
		return time.Time{}, ErrInvalidToken
	}

	// Handle both float64 and int64
	var expFloat float64
	switch v := exp.(type) {
	case float64:
		expFloat = v
	case int64:
		expFloat = float64(v)
	default:
		return time.Time{}, ErrInvalidToken
	}

	return time.Unix(int64(expFloat), 0), nil
}

// Helper manages API client creation and authentication.
type Helper struct {
	config    *config.Config
	tokenPath string
	apiClient *client.SolidPingClient
	verbose   bool
}

// NewHelper creates a new API helper.
func NewHelper(cfg *config.Config, tokenPath string, verbose bool) *Helper {
	return &Helper{
		config:    cfg,
		tokenPath: tokenPath,
		verbose:   verbose,
	}
}

// GetClient returns an authenticated API client.
func (h *Helper) GetClient(ctx context.Context) (*client.SolidPingClient, error) {
	if h.apiClient != nil {
		return h.apiClient, nil
	}

	token, err := h.resolveToken(ctx)
	if err != nil {
		return nil, err
	}

	cfg := client.Config{
		BaseURL: h.config.URL,
		Token:   token,
		Verbose: h.verbose,
	}

	h.apiClient, err = client.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}

	return h.apiClient, nil
}

// ResetClient clears the cached API client, forcing re-authentication on next GetClient call.
func (h *Helper) ResetClient() {
	h.apiClient = nil
}

// TryAuthRecovery attempts to recover from authentication failure.
// It tries refresh token, then auto-login, then prompts for credentials.
// Returns a new token if successful.
func (h *Helper) TryAuthRecovery(ctx context.Context) (string, error) {
	// Try 1: Refresh token
	tokenData, err := h.readTokenFile()
	if err == nil && tokenData != nil && tokenData.IsRefreshTokenValid() {
		newTokenData, refreshErr := h.refreshAccessToken(ctx, tokenData)
		if refreshErr == nil {
			h.ResetClient() // Clear cached client to use new token
			return newTokenData.AccessToken, nil
		}
	}

	// Try 2: Auto-login with stored credentials
	if h.config.Auth.Email != "" && h.config.Auth.Password != "" {
		loginTokenData, loginErr := h.autoLogin(ctx)
		if loginErr == nil {
			h.ResetClient() // Clear cached client to use new token
			return loginTokenData.AccessToken, nil
		}
	}

	// Try 3: Prompt for credentials
	promptTokenData, promptErr := h.promptForCredentials(ctx)
	if promptErr != nil {
		return "", promptErr
	}

	h.ResetClient() // Clear cached client to use new token
	return promptTokenData.AccessToken, nil
}

// createTokenData creates TokenData from login response tokens.
func createTokenData(accessToken, refreshToken string) (*TokenData, error) {
	accessExpires, err := parseJWTExpiration(accessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to parse access token expiration: %w", err)
	}

	var refreshExpires time.Time
	if refreshToken != "" {
		refreshExpires, err = parseJWTExpiration(refreshToken)
		if err != nil {
			// If refresh token parsing fails, set a zero time but don't fail
			// The access token is more important
			refreshExpires = time.Time{}
		}
	}

	return &TokenData{
		AccessToken:           accessToken,
		AccessTokenExpiresAt:  accessExpires,
		RefreshToken:          refreshToken,
		RefreshTokenExpiresAt: refreshExpires,
	}, nil
}

// refreshAccessToken attempts to refresh the access token using the refresh token.
func (h *Helper) refreshAccessToken(ctx context.Context, tokenData *TokenData) (*TokenData, error) {
	if !tokenData.IsRefreshTokenValid() {
		return nil, ErrRefreshTokenInvalid
	}

	// Create client without token for refresh
	cfg := client.Config{
		BaseURL: h.config.URL,
		Verbose: h.verbose,
	}

	tmpClient, err := client.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	// Call refresh endpoint
	resp, err := tmpClient.Refresh(ctx, tokenData.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("refresh token failed: %w", err)
	}

	// Extract new access token
	if resp.AccessToken == nil {
		return nil, ErrNoAccessToken
	}

	// Keep the existing refresh token (refresh endpoint only returns new access token)
	newTokenData, err := createTokenData(*resp.AccessToken, tokenData.RefreshToken)
	if err != nil {
		return nil, err
	}

	// Save the new token data
	if err := h.saveTokenFile(newTokenData); err != nil {
		return nil, fmt.Errorf("failed to save refreshed token: %w", err)
	}

	return newTokenData, nil
}

// resolveToken gets token from file, PAT, or auto-login.
func (h *Helper) resolveToken(ctx context.Context) (string, error) {
	// Priority 1: Token file (saved PAT, valid JWT, or a refreshable JWT).
	if token, ok := h.resolveFromTokenFile(ctx); ok {
		return token, nil
	}

	// Priority 2: PAT from config
	if h.config.Auth.PAT != "" {
		return h.config.Auth.PAT, nil
	}

	// Priority 3: Auto-login with email/password
	if h.config.Auth.Email != "" && h.config.Auth.Password != "" {
		loginTokenData, loginErr := h.autoLogin(ctx)
		if loginErr != nil {
			return "", fmt.Errorf("auto-login failed: %w", loginErr)
		}
		return loginTokenData.AccessToken, nil
	}

	return "", ErrNoAuthentication
}

// resolveFromTokenFile returns a usable credential from the token file, if any:
// a saved PAT verbatim, a still-valid access token, or a freshly refreshed one.
// The bool is false when the file is absent/unreadable or holds nothing usable,
// letting resolveToken fall through to its other credential sources.
func (h *Helper) resolveFromTokenFile(ctx context.Context) (string, bool) {
	tokenData, err := h.readTokenFile()
	if err != nil || tokenData == nil {
		return "", false
	}

	// A saved PAT (browser login) is used verbatim — it does not expire
	// client-side and there is nothing to refresh.
	if tokenData.PAT != "" {
		return tokenData.PAT, true
	}

	if tokenData.IsAccessTokenValid() {
		return tokenData.AccessToken, true
	}

	// Access token expired: try a refresh, else fall through.
	if tokenData.IsRefreshTokenValid() {
		if newTokenData, refreshErr := h.refreshAccessToken(ctx, tokenData); refreshErr == nil {
			return newTokenData.AccessToken, true
		}
	}

	return "", false
}

// autoLogin performs automatic login with configured credentials.
func (h *Helper) autoLogin(ctx context.Context) (*TokenData, error) {
	// Create client without token for login
	cfg := client.Config{
		BaseURL: h.config.URL,
		Verbose: h.verbose,
	}

	tmpClient, err := client.New(cfg)
	if err != nil {
		return nil, err
	}

	// Perform login
	resp, err := tmpClient.Login(ctx, h.config.Org, h.config.Auth.Email, h.config.Auth.Password)
	if err != nil {
		return nil, err
	}

	if resp.AccessToken == nil {
		return nil, ErrNoAccessToken
	}

	// Extract refresh token (may be nil)
	refreshToken := ""
	if resp.RefreshToken != nil {
		refreshToken = *resp.RefreshToken
	}

	// Create token data with expiration times
	tokenData, err := createTokenData(*resp.AccessToken, refreshToken)
	if err != nil {
		return nil, err
	}

	// Save token data for future use
	if err := h.saveTokenFile(tokenData); err != nil {
		return nil, fmt.Errorf("failed to save token: %w", err)
	}

	return tokenData, nil
}

// promptForCredentials prompts the user to enter credentials manually.
func (h *Helper) promptForCredentials(ctx context.Context) (*TokenData, error) {
	fmt.Fprintln(os.Stderr, "\nAutomatic authentication failed. Please enter your credentials:")

	reader := bufio.NewReader(os.Stdin)

	// Prompt for email
	fmt.Fprint(os.Stderr, "Email: ")
	email, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read email: %w", err)
	}
	email = strings.TrimSpace(email)

	// Prompt for password (hidden input)
	fmt.Fprint(os.Stderr, "Password: ")
	passwordBytes, err := term.ReadPassword(syscall.Stdin)
	if err != nil {
		return nil, fmt.Errorf("failed to read password: %w", err)
	}
	fmt.Fprintln(os.Stderr) // New line after password input
	password := string(passwordBytes)

	// Create client without token for login
	cfg := client.Config{
		BaseURL: h.config.URL,
		Verbose: h.verbose,
	}

	tmpClient, err := client.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	// Perform login
	resp, err := tmpClient.Login(ctx, h.config.Org, email, password)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}

	if resp.AccessToken == nil {
		return nil, ErrNoAccessToken
	}

	// Extract refresh token (may be nil)
	refreshToken := ""
	if resp.RefreshToken != nil {
		refreshToken = *resp.RefreshToken
	}

	// Create token data with expiration times
	tokenData, err := createTokenData(*resp.AccessToken, refreshToken)
	if err != nil {
		return nil, err
	}

	// Save token data for future use
	if err := h.saveTokenFile(tokenData); err != nil {
		return nil, fmt.Errorf("failed to save token: %w", err)
	}

	fmt.Fprintln(os.Stderr, "✓ Login successful")

	return tokenData, nil
}

// Login performs login and saves token.
func (h *Helper) Login(ctx context.Context, org, email, password string) (string, *client.UserSummary, error) {
	// Create client without token for login
	cfg := client.Config{
		BaseURL: h.config.URL,
		Verbose: h.verbose,
	}

	tmpClient, err := client.New(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create client: %w", err)
	}

	// Perform login
	resp, err := tmpClient.Login(ctx, org, email, password)
	if err != nil {
		return "", nil, err
	}

	if resp.AccessToken == nil {
		return "", resp.User, ErrNoAccessToken
	}

	// Extract refresh token (may be nil)
	refreshToken := ""
	if resp.RefreshToken != nil {
		refreshToken = *resp.RefreshToken
	}

	// Create token data with expiration times
	tokenData, err := createTokenData(*resp.AccessToken, refreshToken)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create token data: %w", err)
	}

	// Save complete token data
	if err := h.saveTokenFile(tokenData); err != nil {
		return "", nil, fmt.Errorf("failed to save token: %w", err)
	}

	// A forced rotation does NOT make the login fail server-side — the whole
	// design is that the session exists and can reach the rotation endpoint.
	// But reporting "Login successful!" and then failing every subsequent
	// command with a 403 is the opaque outcome this check exists to avoid: say
	// it here, at the moment the operator is looking at a password prompt.
	// The token is deliberately kept on disk — it is the credential the
	// rotation itself needs.
	if mustChange, checkErr := tmpClient.SessionRequiresPasswordChange(ctx); checkErr == nil && mustChange {
		return "", resp.User, client.ErrPasswordChangeRequired
	}

	return tokenData.AccessToken, resp.User, nil
}

// Logout removes saved token and optionally calls logout API.
func (h *Helper) Logout(ctx context.Context, callAPI bool) error {
	// Optionally call logout API
	if callAPI {
		// Drop the OAuth grant server-side (RFC 7009) before discarding the
		// local credentials, so a finished CLI session doesn't leave a live,
		// refreshable grant lingering until its TTL.
		h.revokeStoredGrant(ctx)

		if apiClient, err := h.GetClient(ctx); err == nil {
			// Best effort - ignore errors
			_, _ = apiClient.Logout(ctx, false)
		}
	}

	// Remove token file
	return h.deleteTokenFile()
}

// revokeStoredGrant best-effort revokes the stored OAuth refresh grant via the
// RFC 7009 revocation endpoint. The endpoint only deletes oauth_refresh grants
// bound to the CLI client, so it is a safe no-op for the other credential types
// the CLI may hold (PATs, session refresh tokens). Errors are swallowed —
// logout must still succeed locally when offline or already-revoked.
func (h *Helper) revokeStoredGrant(ctx context.Context) {
	tokenData, err := h.readTokenFile()
	if err != nil || tokenData == nil || tokenData.RefreshToken == "" {
		return
	}

	form := url.Values{}
	form.Set("token", tokenData.RefreshToken)
	form.Set("client_id", oauth.CLIClientID)

	endpoint := strings.TrimRight(h.config.URL, "/") + oauth.PathRevoke

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}

	_ = resp.Body.Close()
}

// SaveTokens saves new access and refresh tokens to the token file.
func (h *Helper) SaveTokens(accessToken, refreshToken string) error {
	tokenData, err := createTokenData(accessToken, refreshToken)
	if err != nil {
		return err
	}
	h.ResetClient()
	return h.saveTokenFile(tokenData)
}

// SavePAT saves a Personal Access Token as the sole credential in the token
// file, replacing any prior JWT session. Used by the browser-login flow after
// self-minting a PAT, and by the `--token` / SP_TOKEN paste fallback.
func (h *Helper) SavePAT(pat string) error {
	h.ResetClient()
	return h.saveTokenFile(&TokenData{PAT: pat})
}

// AuthMethod reports how the currently-resolved credential authenticates:
// "pat" for a Personal Access Token, "jwt" otherwise. It resolves the token the
// same way GetClient does, so it reflects the credential a request would use.
func (h *Helper) AuthMethod(ctx context.Context) string {
	token, err := h.resolveToken(ctx)
	if err == nil && strings.HasPrefix(token, patTokenPrefix) {
		return "pat"
	}

	return "jwt"
}

// readTokenFile reads the token data from the token file.
// Returns nil TokenData if no token file exists (not an error).
func (h *Helper) readTokenFile() (*TokenData, error) {
	data, err := os.ReadFile(h.tokenPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil //nolint:nilnil // No token file is a valid state, not an error
		}
		return nil, err
	}

	// Parse JSON format
	var tokenData TokenData
	if err := json.Unmarshal(data, &tokenData); err != nil {
		return nil, fmt.Errorf("failed to parse token file: %w", err)
	}

	return &tokenData, nil
}

// saveTokenFile saves the token data to the token file in JSON format.
func (h *Helper) saveTokenFile(tokenData *TokenData) error {
	// Ensure directory exists
	dir := filepath.Dir(h.tokenPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Marshal to JSON with indentation for readability
	data, err := json.MarshalIndent(tokenData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal token data: %w", err)
	}

	// Write token with restricted permissions
	return os.WriteFile(h.tokenPath, data, 0o600)
}

// deleteTokenFile removes the token file.
func (h *Helper) deleteTokenFile() error {
	err := os.Remove(h.tokenPath)
	if os.IsNotExist(err) {
		return nil // Already deleted
	}
	return err
}
