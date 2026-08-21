package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// TokenResponse is Discord's OAuth2 token-exchange response. For a `bot`
// authorization it also carries the guild the bot was added to, which is the
// only place the install learns which server it just joined.
//
//nolint:tagliatelle // Discord API uses snake_case
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Guild        *Guild `json:"guild,omitempty"`
}

// ExchangeCode trades an authorization code for tokens. tokenURL is a
// parameter so tests can point it at an httptest stand-in and still exercise
// the real request shape.
func ExchangeCode(
	ctx context.Context, tokenURL, clientID, clientSecret, code, redirectURI string,
) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("building discord token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling discord token endpoint: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("reading discord token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d: %s", ErrUnexpectedStatus, resp.StatusCode, string(body))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parsing discord token response: %w", err)
	}

	return &tokenResp, nil
}

// FetchCurrentUser resolves the user behind a *user* access token (the
// `identify` scope). Used only to record who installed the bot.
func FetchCurrentUser(ctx context.Context, apiBaseURL, userAccessToken string) (*User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+"/users/@me", nil)
	if err != nil {
		return nil, fmt.Errorf("building discord user request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userAccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling discord users/@me: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d on users/@me", ErrUnexpectedStatus, resp.StatusCode)
	}

	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("parsing discord user: %w", err)
	}

	return &user, nil
}
