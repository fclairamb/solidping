package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/oauth"
	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

const (
	// browserLoginTimeout bounds how long the CLI waits for the human to finish
	// the browser login before giving up on the loopback callback.
	browserLoginTimeout = 5 * time.Minute
	// cliPATExpiry is the lifetime of the PAT self-minted by the browser flow.
	// It matches the dashboard's default token expiry (90 days).
	cliPATExpiry = 90 * 24 * time.Hour
	// pkceVerifierBytes is the entropy behind the PKCE verifier; 32 bytes
	// base64url-encode to a 43-char verifier, the RFC 7636 minimum.
	pkceVerifierBytes = 32
	// stateBytes is the entropy behind the anti-CSRF state parameter.
	stateBytes = 16
)

// Sentinel errors for the browser login flow.
var (
	// errStateMismatch means the loopback callback carried a state value that
	// did not match the one the CLI generated — a possible CSRF, so we abort.
	errStateMismatch = errors.New("oauth state mismatch (possible CSRF), aborting login")
	// errNoAuthCode means the callback arrived without an authorization code.
	errNoAuthCode = errors.New("no authorization code received from the browser")
	// errAuthorizationDenied means the authorization server redirected back an
	// OAuth error (e.g. the user denied consent).
	errAuthorizationDenied = errors.New("authorization was denied")
	// errNoAccessToken means the token endpoint returned no access token.
	errNoAccessToken = errors.New("token endpoint returned no access token")
	// errNoOrg means the org could not be resolved from the session.
	errNoOrg = errors.New("could not determine the organization for this session")
)

// browserLoginResult carries what the login action needs to report success.
type browserLoginResult struct {
	Org       string
	UserEmail string
	UserUID   string
	TokenName string
}

// authBrowserLogin runs the full RFC 8252 loopback browser login: it opens the
// system browser at the authorization endpoint, receives the one-time code on
// an ephemeral 127.0.0.1 listener, exchanges it for an OAuth access token,
// self-mints a named PAT with that token, and saves only the PAT. The OAuth
// access/refresh tokens are discarded — only the one-time code ever transits the
// loopback redirect.
func authBrowserLogin(ctx context.Context, cliCtx *Context) (*browserLoginResult, error) {
	baseURL := strings.TrimRight(cliCtx.Config.URL, "/")

	// Ephemeral loopback listener; the OS picks a free port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bind loopback listener: %w", err)
	}
	defer func() { _ = listener.Close() }()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return nil, fmt.Errorf("unexpected listener address type %T", listener.Addr())
	}
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", tcpAddr.Port, oauth.CLICallbackPath)

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, err
	}

	state, err := generateState()
	if err != nil {
		return nil, err
	}

	authorizeURL := buildAuthorizeURL(baseURL, redirectURI, challenge, state)

	// Serve the loopback callback while the human logs in.
	callback := &loopbackCallback{state: state, resultCh: make(chan callbackResult, 1)}
	srv := &http.Server{Handler: callback.handler(), ReadHeaderTimeout: 10 * time.Second}

	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	output.PrintMessage(os.Stdout, "Opening your browser to log in...")
	if openErr := openBrowser(authorizeURL); openErr != nil {
		output.PrintMessage(os.Stdout, "Could not open a browser automatically.")
	}
	output.PrintMessage(os.Stdout, "If it doesn't open, visit this URL:")
	output.PrintMessage(os.Stdout, "  "+authorizeURL)

	code, err := callback.wait(ctx, browserLoginTimeout)
	if err != nil {
		return nil, err
	}

	accessToken, err := exchangeCodeForToken(ctx, baseURL, code, redirectURI, verifier, cliCtx.Verbose)
	if err != nil {
		return nil, err
	}

	// Use the short-lived OAuth access token to resolve the org and self-mint a
	// durable PAT, then throw the OAuth token away.
	return finishBrowserLogin(ctx, cliCtx, baseURL, accessToken)
}

// finishBrowserLogin resolves the org from /auth/me, self-mints a PAT, and saves
// it as the sole credential.
func finishBrowserLogin(
	ctx context.Context, cliCtx *Context, baseURL, accessToken string,
) (*browserLoginResult, error) {
	oauthClient, err := client.New(client.Config{
		BaseURL: baseURL,
		Token:   accessToken,
		Verbose: cliCtx.Verbose,
	})
	if err != nil {
		return nil, fmt.Errorf("create API client: %w", err)
	}

	me, err := oauthClient.Me(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve current user: %w", err)
	}

	org := ""
	if me.Organization != nil && me.Organization.Slug != nil {
		org = *me.Organization.Slug
	}

	if org == "" {
		return nil, errNoOrg
	}

	tokenName := cliPATTokenName()
	expiresAt := time.Now().Add(cliPATExpiry)

	patResp, err := oauthClient.CreateToken(ctx, org, tokenName, &expiresAt)
	if err != nil {
		return nil, fmt.Errorf("mint personal access token: %w", err)
	}

	if patResp.Token == nil || *patResp.Token == "" {
		return nil, fmt.Errorf("mint personal access token: %w", errNoAccessToken)
	}

	if saveErr := cliCtx.APIHelper.SavePAT(*patResp.Token); saveErr != nil {
		return nil, fmt.Errorf("save token: %w", saveErr)
	}

	result := &browserLoginResult{Org: org, TokenName: tokenName}
	if me.User != nil {
		if me.User.Email != nil {
			result.UserEmail = string(*me.User.Email)
		}
		if me.User.Uid != nil {
			result.UserUID = me.User.Uid.String()
		}
	}

	return result, nil
}

// generatePKCE returns a random RFC 7636 code_verifier and its S256
// code_challenge, both base64url-encoded without padding.
func generatePKCE() (verifier, challenge string, err error) {
	buf := make([]byte, pkceVerifierBytes)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate pkce verifier: %w", err)
	}

	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])

	return verifier, challenge, nil
}

// generateState returns a random anti-CSRF state parameter.
func generateState() (string, error) {
	buf := make([]byte, stateBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// buildAuthorizeURL assembles the /authorize URL for the pre-registered CLI
// client. scope and resource are intentionally omitted so the server applies its
// defaults (full "mcp" scope, the MCP resource audience).
func buildAuthorizeURL(baseURL, redirectURI, challenge, state string) string {
	query := url.Values{}
	query.Set("response_type", oauth.ResponseTypeCode)
	query.Set("client_id", oauth.CLIClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", oauth.CodeChallengeMethodS256)
	query.Set("state", state)

	return baseURL + oauth.PathAuthorize + "?" + query.Encode()
}

// callbackResult is the outcome delivered by the loopback callback: an
// authorization code on success, or an error.
type callbackResult struct {
	code string
	err  error
}

// loopbackCallback serves the one-shot RFC 8252 redirect endpoint. It verifies
// the state, captures the authorization code, renders a close-tab page, and
// delivers the result exactly once over resultCh.
type loopbackCallback struct {
	state    string
	resultCh chan callbackResult
	once     sync.Once
}

// handler returns the HTTP handler that serves the callback path.
func (c *loopbackCallback) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(oauth.CLICallbackPath, c.serve)

	return mux
}

// serve handles the redirect from the authorization server.
func (c *loopbackCallback) serve(writer http.ResponseWriter, req *http.Request) {
	query := req.URL.Query()

	if oauthErr := query.Get("error"); oauthErr != "" {
		desc := query.Get("error_description")
		c.finish(callbackResult{err: fmt.Errorf("%w: %s %s", errAuthorizationDenied, oauthErr, desc)})
		writeCallbackPage(writer, false, "Authorization failed. You can close this tab.")

		return
	}

	if query.Get("state") != c.state {
		c.finish(callbackResult{err: errStateMismatch})
		writeCallbackPage(writer, false, "State mismatch. You can close this tab.")

		return
	}

	code := query.Get("code")
	if code == "" {
		c.finish(callbackResult{err: errNoAuthCode})
		writeCallbackPage(writer, false, "No authorization code received. You can close this tab.")

		return
	}

	c.finish(callbackResult{code: code})
	writeCallbackPage(writer, true, "Login successful! You can close this tab and return to the terminal.")
}

// finish delivers the result once; later callbacks (e.g. a favicon fetch) are
// dropped.
func (c *loopbackCallback) finish(res callbackResult) {
	c.once.Do(func() { c.resultCh <- res })
}

// wait blocks until the callback fires, the timeout elapses, or ctx is
// cancelled, returning the authorization code or an error.
func (c *loopbackCallback) wait(ctx context.Context, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case res := <-c.resultCh:
		return res.code, res.err
	case <-ctx.Done():
		return "", fmt.Errorf("waiting for browser login: %w", ctx.Err())
	}
}

// writeCallbackPage renders the minimal page the human sees after approving.
func writeCallbackPage(writer http.ResponseWriter, success bool, message string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")

	if !success {
		writer.WriteHeader(http.StatusBadRequest)
	}

	title := "SolidPing CLI"
	_, _ = io.WriteString(writer, "<!doctype html><html><head><meta charset=\"utf-8\">"+
		"<title>"+title+"</title></head><body style=\"font-family:system-ui,sans-serif;"+
		"text-align:center;padding-top:4rem\"><h1>"+title+"</h1><p>"+message+"</p></body></html>")
}

// exchangeCodeForToken redeems the authorization code at /oauth/token for an
// access token (PKCE, public client). It posts the RFC 6749 form directly since
// the token endpoint is not part of the generated API client.
func exchangeCodeForToken(
	ctx context.Context, baseURL, code, redirectURI, verifier string, verbose bool,
) (string, error) {
	form := url.Values{}
	form.Set("grant_type", oauth.GrantAuthorizationCode)
	form.Set("code", code)
	form.Set("client_id", oauth.CLIClientID)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, baseURL+oauth.PathToken, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token exchange request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read token response: %w", err)
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "token endpoint status %d\n", resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	//nolint:tagliatelle // RFC 6749 wire format requires snake_case field names.
	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return "", errNoAccessToken
	}

	return tokenResp.AccessToken, nil
}

// cliPATTokenName names the self-minted PAT after the local host so it is easy
// to identify and revoke from the dashboard tokens page.
func cliPATTokenName() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown-host"
	}

	return "sp CLI on " + host
}

// openBrowser opens rawURL in the system default browser. The command name is a
// compile-time constant per OS; only the URL is variable.
func openBrowser(rawURL string) error {
	var name string

	var args []string

	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{rawURL}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}
	default:
		name, args = "xdg-open", []string{rawURL}
	}

	//nolint:gosec // name is a fixed per-OS constant; only the URL argument varies.
	if err := exec.Command(name, args...).Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}

	return nil
}
