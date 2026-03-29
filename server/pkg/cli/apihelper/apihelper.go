// Package apihelper provides helper functions for API client operations.
package apihelper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/term"

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

// TokenData represents the stored token information.
type TokenData struct {
	AccessToken           string    `json:"accessToken"`
	AccessTokenExpiresAt  time.Time `json:"accessTokenExpiresAt"`
	RefreshToken          string    `json:"refreshToken"`
	RefreshTokenExpiresAt time.Time `json:"refreshTokenExpiresAt"`
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
	// Priority 1: Token file (with automatic refresh)
	tokenData, err := h.readTokenFile()
	if err == nil && tokenData != nil {
		// If access token is still valid, use it
		if tokenData.IsAccessTokenValid() {
			return tokenData.AccessToken, nil
		}

		// Access token expired, try to refresh it
		if tokenData.IsRefreshTokenValid() {
			newTokenData, refreshErr := h.refreshAccessToken(ctx, tokenData)
			if refreshErr == nil {
				return newTokenData.AccessToken, nil
			}
			// Refresh failed, continue to fallback methods
		}
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

	return tokenData.AccessToken, resp.User, nil
}

// Logout removes saved token and optionally calls logout API.
func (h *Helper) Logout(ctx context.Context, callAPI bool) error {
	// Optionally call logout API
	if callAPI {
		if apiClient, err := h.GetClient(ctx); err == nil {
			// Best effort - ignore errors
			_, _ = apiClient.Logout(ctx, false)
		}
	}

	// Remove token file
	return h.deleteTokenFile()
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
