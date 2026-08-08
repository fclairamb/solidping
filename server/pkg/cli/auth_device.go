package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// OAuth 2.0 Device Authorization Grant (RFC 8628) client — the default
// `sp auth login` flow, and the only browser-based one.
//
// It works everywhere the old loopback flow did NOT: over SSH, inside
// containers, on headless servers. The CLI shows a short code, the human
// approves it in any browser where they are already logged in (their phone
// counts), and the CLI polls until the server hands back a named PAT.
//
// The wire format here is snake_case because that is what RFC 8628 specifies;
// it deliberately deviates from solidping's house camelCase style, and the
// server side does the same.
const (
	// devicePathAuthorize opens a device authorization request.
	devicePathAuthorize = "/api/v1/auth/device"
	// devicePathToken is the polling endpoint.
	devicePathToken = "/api/v1/auth/device/token"
	// deviceGrantType is the RFC 8628 grant_type.
	deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// deviceLoginTimeout bounds the whole flow if the server never advertises
	// a usable expires_in.
	deviceLoginTimeout = 15 * time.Minute
	// deviceMinPollInterval floors the server-advertised interval so a
	// misconfigured server cannot make the CLI hammer the endpoint.
	deviceMinPollInterval = 2 * time.Second
	// deviceSlowDownStep is how much the client backs off after a slow_down,
	// per RFC 8628 §3.5.
	deviceSlowDownStep = 5 * time.Second
	// deviceHTTPTimeout bounds a single request in the flow.
	deviceHTTPTimeout = 30 * time.Second
)

// Sentinel errors for the device login flow.
var (
	// errDeviceAccessDenied means the human denied the request in the browser.
	errDeviceAccessDenied = errors.New("the login request was denied")
	// errDeviceExpired means the request expired before anyone approved it.
	errDeviceExpired = errors.New("the login request expired before it was approved")
	// errDeviceNoToken means the token endpoint returned no access token.
	errDeviceNoToken = errors.New("the server returned no access token")
	// errDeviceUnexpected wraps an unexpected OAuth error code.
	errDeviceUnexpected = errors.New("device authorization failed")
	// errNoOrg means the org could not be resolved from the minted token.
	errNoOrg = errors.New("could not determine the organization for this session")
)

// deviceLoginResult carries what the login action needs to report success.
type deviceLoginResult struct {
	Org       string
	UserEmail string
	UserUID   string
	TokenName string
}

// deviceAuthorizationResponse is the RFC 8628 §3.2 response.
//
//nolint:tagliatelle // RFC 8628 wire format requires snake_case field names.
type deviceAuthorizationResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// deviceTokenResponse is the RFC 8628 §3.5 success/error union: either
// access_token is set, or error is.
//
//nolint:tagliatelle // RFC 6749/8628 wire format requires snake_case field names.
type deviceTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// authDeviceLogin runs the full device authorization flow: open a request,
// show the user code, best-effort open a browser at the complete verification
// URL (GitHub-style), poll until approval or expiry, and persist the PAT the
// server minted at consent time.
func authDeviceLogin(ctx context.Context, cliCtx *Context) (*deviceLoginResult, error) {
	baseURL := strings.TrimRight(cliCtx.Config.URL, "/")
	tokenName := cliPATTokenName()

	authz, err := startDeviceAuthorization(ctx, baseURL, tokenName, cliCtx.Verbose)
	if err != nil {
		return nil, err
	}

	announceDeviceCode(cliCtx, authz)

	if openErr := openBrowser(ctx, authz.VerificationURIComplete); openErr != nil {
		output.PrintMessage(os.Stdout, "Could not open a browser automatically — open the URL above yourself.")
	}

	patToken, err := pollDeviceToken(ctx, baseURL, authz, cliCtx.Verbose)
	if err != nil {
		return nil, err
	}

	return finishDeviceLogin(ctx, cliCtx, baseURL, patToken, tokenName)
}

// announceDeviceCode prints the code + URL the human needs. Text mode only —
// in JSON mode the flow is still interactive, so the prompt goes to stderr
// rather than corrupting the machine-readable stdout payload.
func announceDeviceCode(cliCtx *Context, authz *deviceAuthorizationResponse) {
	target := os.Stdout
	if !cliCtx.IsText() {
		target = os.Stderr
	}

	output.PrintMessage(target, "")
	output.PrintMessage(target, "First copy your one-time code: "+authz.UserCode)
	output.PrintMessage(target, "Then open this URL to approve the login:")
	output.PrintMessage(target, "  "+authz.VerificationURI)
	output.PrintMessage(target, "")
	output.PrintMessage(target, "Waiting for approval...")
}

// startDeviceAuthorization opens the request and returns the server's
// RFC 8628 parameters.
func startDeviceAuthorization(
	ctx context.Context, baseURL, clientName string, verbose bool,
) (*deviceAuthorizationResponse, error) {
	payload, err := json.Marshal(map[string]string{"clientName": clientName})
	if err != nil {
		return nil, fmt.Errorf("encode device authorization request: %w", err)
	}

	body, status, err := postDeviceJSON(ctx, baseURL+devicePathAuthorize, payload, verbose)
	if err != nil {
		return nil, err
	}

	if status != http.StatusOK {
		return nil, fmt.Errorf("%w (status %d): %s", errDeviceUnexpected, status, strings.TrimSpace(string(body)))
	}

	var authz deviceAuthorizationResponse
	if err := json.Unmarshal(body, &authz); err != nil {
		return nil, fmt.Errorf("decode device authorization response: %w", err)
	}

	if authz.DeviceCode == "" || authz.UserCode == "" {
		return nil, fmt.Errorf("%w: incomplete device authorization response", errDeviceUnexpected)
	}

	return &authz, nil
}

// pollDeviceToken polls the token endpoint until the request is approved,
// denied, or expires. It honors the server-advertised interval and backs off
// further on slow_down, per RFC 8628 §3.5.
func pollDeviceToken(
	ctx context.Context, baseURL string, authz *deviceAuthorizationResponse, verbose bool,
) (string, error) {
	interval := time.Duration(authz.Interval) * time.Second
	if interval < deviceMinPollInterval {
		interval = deviceMinPollInterval
	}

	deadline := deviceLoginTimeout
	if authz.ExpiresIn > 0 {
		deadline = time.Duration(authz.ExpiresIn) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	payload, err := json.Marshal(map[string]string{
		"grant_type":  deviceGrantType,
		"device_code": authz.DeviceCode,
	})
	if err != nil {
		return "", fmt.Errorf("encode device token request: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return "", errDeviceExpired
		case <-time.After(interval):
		}

		body, _, err := postDeviceJSON(ctx, baseURL+devicePathToken, payload, verbose)
		if err != nil {
			if ctx.Err() != nil {
				return "", errDeviceExpired
			}

			return "", err
		}

		token, backOff, err := interpretDeviceTokenResponse(body)
		if err != nil {
			return "", err
		}

		if token != "" {
			return token, nil
		}

		if backOff {
			interval += deviceSlowDownStep
		}
	}
}

// interpretDeviceTokenResponse decodes one token-endpoint reply. It returns the
// access token once granted, a back-off flag on slow_down, and a terminal error
// for every RFC 8628 outcome the client must stop on. An empty token with no
// error and no back-off means "keep waiting" (authorization_pending).
func interpretDeviceTokenResponse(body []byte) (string, bool, error) {
	var resp deviceTokenResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", false, fmt.Errorf("decode device token response: %w", err)
	}

	if resp.Error == "" {
		if resp.AccessToken == "" {
			return "", false, errDeviceNoToken
		}

		return resp.AccessToken, false, nil
	}

	switch resp.Error {
	case "authorization_pending":
		return "", false, nil
	case "slow_down":
		return "", true, nil
	case "access_denied":
		return "", false, errDeviceAccessDenied
	case "expired_token":
		return "", false, errDeviceExpired
	default:
		return "", false, fmt.Errorf("%w: %s %s", errDeviceUnexpected, resp.Error, resp.ErrorDescription)
	}
}

// postDeviceJSON posts a JSON body and returns the raw response body and
// status. The device endpoints are unauthenticated, so no credential is
// attached.
func postDeviceJSON(ctx context.Context, url string, payload []byte, verbose bool) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: deviceHTTPTimeout}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("device request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read device response: %w", err)
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "%s -> status %d\n", url, resp.StatusCode)
	}

	return body, resp.StatusCode, nil
}

// finishDeviceLogin persists the PAT and resolves the identity behind it for
// the success message.
func finishDeviceLogin(
	ctx context.Context, cliCtx *Context, baseURL, patToken, tokenName string,
) (*deviceLoginResult, error) {
	if err := cliCtx.APIHelper.SavePAT(patToken); err != nil {
		return nil, fmt.Errorf("save token: %w", err)
	}

	apiClient, err := client.New(client.Config{
		BaseURL: baseURL,
		Token:   patToken,
		Verbose: cliCtx.Verbose,
	})
	if err != nil {
		return nil, fmt.Errorf("create API client: %w", err)
	}

	meResp, err := apiClient.Me(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve current user: %w", err)
	}

	result := &deviceLoginResult{
		Org:       orgSlugFromMe(meResp),
		UserEmail: userEmailFromMe(meResp),
		UserUID:   userUIDFromMe(meResp),
		TokenName: tokenName,
	}

	if result.Org == "" {
		return nil, errNoOrg
	}

	return result, nil
}

// cliPATTokenName names the PAT the server mints at consent time after the
// local host, so it is easy to identify and revoke from the dashboard tokens
// page.
func cliPATTokenName() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown-host"
	}

	return "sp CLI on " + host
}

// openBrowser opens rawURL in the system default browser. The command name is a
// compile-time constant per OS; only the URL is variable.
func openBrowser(ctx context.Context, rawURL string) error {
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

	if err := exec.CommandContext(ctx, name, args...).Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}

	return nil
}
