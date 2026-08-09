// Package cli provides command-line interface functionality for SolidPing.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/fclairamb/solidping/server/pkg/cli/config"
	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// authLoginAction handles the login command. The OAuth 2.0 device
// authorization flow (RFC 8628) is the default — it is the only browser flow
// that also works over SSH, in containers and on headless boxes.
// --with-password / --email / --password and --token are the explicit
// non-browser fallbacks.
func authLoginAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	email := cmd.String("email")
	password := cmd.String("password")
	token := strings.TrimSpace(cmd.String("token"))
	withPassword := cmd.Bool("with-password")

	switch {
	case token != "":
		// Headless: the human pasted a PAT created on the dashboard.
		return authLoginWithToken(ctx, cliCtx, token)
	case withPassword || email != "" || password != "":
		// Classic credentials (scripts, self-hosted without SSO).
		return authLoginWithPassword(ctx, cliCtx, email, password)
	default:
		// Default: device flow — approve in any browser, end up holding only
		// a named PAT.
		return authLoginWithDevice(ctx, cliCtx)
	}
}

// authLoginWithPassword performs the classic email/password login, prompting for
// whatever flag was omitted.
func authLoginWithPassword(ctx context.Context, cliCtx *Context, email, password string) error {
	var err error

	if email == "" {
		if email, err = promptForInput("Email: "); err != nil {
			return err
		}
	}

	if password == "" {
		if password, err = readPassword("Password: "); err != nil {
			return err
		}
	}

	_, user, err := cliCtx.APIHelper.Login(ctx, cliCtx.GetOrg(), email, password)
	if err != nil {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(fmt.Errorf("login failed: %w", err))
		}
		output.PrintError(os.Stdout, fmt.Sprintf("Login failed: %v", err))
		return cli.Exit("", 3) // Authentication error
	}

	userMap := map[string]interface{}{}
	if user != nil {
		if user.Uid != nil {
			userMap["uid"] = user.Uid.String()
		}
		if user.Email != nil {
			userMap["email"] = *user.Email
		}
	}

	if !cliCtx.IsText() {
		tokenPath, _ := config.TokenPath()
		return cliCtx.Outputter.Print(map[string]interface{}{
			keySuccess:    true,
			keyUser:       userMap,
			keyAuthMethod: authMethodJWT,
			"token_path":  tokenPath,
		})
	}

	output.PrintSuccess(os.Stdout, "Login successful!")
	if user != nil && user.Email != nil && user.Uid != nil {
		output.PrintMessage(os.Stdout, fmt.Sprintf("Logged in as: %s (%s)", *user.Email, user.Uid.String()))
	}
	tokenPath, _ := config.TokenPath()
	output.PrintMessage(os.Stdout, "Token saved to: "+tokenPath)

	return nil
}

// authLoginWithToken saves a pasted PAT and confirms it works by calling
// /auth/me. This is the headless path where the browser runs on another machine.
func authLoginWithToken(ctx context.Context, cliCtx *Context, token string) error {
	if err := cliCtx.APIHelper.SavePAT(token); err != nil {
		return cliCtx.HandleError("Failed to save token", err)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.Me(ctx)
	if err != nil {
		return cliCtx.HandleError("Token saved but verification failed", err)
	}

	return printLoginResult(cliCtx, loginSummary{
		org:       orgSlugFromMe(resp),
		userEmail: userEmailFromMe(resp),
		userUID:   userUIDFromMe(resp),
	})
}

// authLoginWithDevice runs the RFC 8628 device authorization flow and reports
// success.
func authLoginWithDevice(ctx context.Context, cliCtx *Context) error {
	result, err := authDeviceLogin(ctx, cliCtx)
	if err != nil {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(fmt.Errorf("login failed: %w", err))
		}
		output.PrintError(os.Stdout, fmt.Sprintf("Login failed: %v", err))
		return cli.Exit("", 3) // Authentication error
	}

	return printLoginResult(cliCtx, loginSummary{
		org:       result.Org,
		userEmail: result.UserEmail,
		userUID:   result.UserUID,
		tokenName: result.TokenName,
	})
}

// loginSummary is the small set of fields the PAT-based login paths report.
type loginSummary struct {
	org       string
	userEmail string
	userUID   string
	tokenName string
}

// printLoginResult renders a successful PAT login in the active output format.
func printLoginResult(cliCtx *Context, summary loginSummary) error {
	tokenPath, _ := config.TokenPath()

	if !cliCtx.IsText() {
		userMap := map[string]interface{}{}
		if summary.userUID != "" {
			userMap["uid"] = summary.userUID
		}
		if summary.userEmail != "" {
			userMap["email"] = summary.userEmail
		}
		out := map[string]interface{}{
			keySuccess:      true,
			keyUser:         userMap,
			keyOrganization: map[string]interface{}{keySlug: summary.org},
			keyAuthMethod:   authMethodPAT,
			"token_path":    tokenPath,
		}
		if summary.tokenName != "" {
			out["token_name"] = summary.tokenName
		}
		return cliCtx.Outputter.Print(out)
	}

	output.PrintSuccess(os.Stdout, "Login successful!")
	if summary.userEmail != "" && summary.userUID != "" {
		output.PrintMessage(os.Stdout, fmt.Sprintf("Logged in as: %s (%s)", summary.userEmail, summary.userUID))
	}
	if summary.org != "" {
		output.PrintMessage(os.Stdout, "Organization: "+summary.org)
	}
	if summary.tokenName != "" {
		output.PrintMessage(os.Stdout, "Token name:   "+summary.tokenName)
	}
	output.PrintMessage(os.Stdout, "Token saved to: "+tokenPath)

	return nil
}

// orgSlugFromMe / userEmailFromMe / userUIDFromMe safely read display fields off
// a /auth/me response.
func orgSlugFromMe(resp *client.MeResponse) string {
	if resp != nil && resp.Organization != nil && resp.Organization.Slug != nil {
		return *resp.Organization.Slug
	}

	return ""
}

func userEmailFromMe(resp *client.MeResponse) string {
	if resp != nil && resp.User != nil && resp.User.Email != nil {
		return string(*resp.User.Email)
	}

	return ""
}

func userUIDFromMe(resp *client.MeResponse) string {
	if resp != nil && resp.User != nil && resp.User.Uid != nil {
		return resp.User.Uid.String()
	}

	return ""
}

// authLogoutAction handles the logout command.
func authLogoutAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Perform logout
	err = cliCtx.APIHelper.Logout(ctx, true) // Call API to invalidate token
	if err != nil {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(fmt.Errorf("logout failed: %w", err))
		}
		output.PrintError(os.Stdout, fmt.Sprintf("Logout failed: %v", err))
		return cli.Exit("", 1)
	}

	// Output success
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(map[string]interface{}{
			keySuccess: true,
			keyMessage: "Logged out successfully",
		})
	}

	output.PrintSuccess(os.Stdout, "Logout successful!")
	tokenPath, _ := config.TokenPath()
	output.PrintMessage(os.Stdout, "Token removed from: "+tokenPath)

	return nil
}

// authMeAction handles the me command
//
//nolint:cyclop,funlen // JSON and text output formatting
func authMeAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Get API client
	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	// Get current user
	resp, err := apiClient.Me(ctx)
	if err != nil {
		return cliCtx.HandleError("Failed to get user info", err)
	}

	// Output user info
	if !cliCtx.IsText() { //nolint:nestif // JSON output construction
		tokenPath, _ := config.TokenPath()
		userMap := map[string]interface{}{}
		if resp.User != nil {
			if resp.User.Uid != nil {
				userMap["uid"] = resp.User.Uid.String()
			}
			if resp.User.Email != nil {
				userMap["email"] = *resp.User.Email
			}
		}
		orgMap := map[string]interface{}{}
		if resp.Organization != nil {
			if resp.Organization.Uid != nil {
				orgMap["uid"] = resp.Organization.Uid.String()
			}
			if resp.Organization.Slug != nil {
				orgMap[keySlug] = *resp.Organization.Slug
			}
		}
		return cliCtx.Outputter.Print(map[string]interface{}{
			keyUser:         userMap,
			keyOrganization: orgMap,
			keyAuthMethod:   cliCtx.APIHelper.AuthMethod(ctx),
			"token_source":  tokenPath,
		})
	}

	// Print user info in human-readable format
	if resp.User != nil {
		if resp.User.Uid != nil {
			output.PrintMessage(os.Stdout, "User ID:       "+resp.User.Uid.String())
		}
		if resp.User.Email != nil {
			output.PrintMessage(os.Stdout, fmt.Sprintf("Email:         %s", *resp.User.Email))
		}
	}
	if resp.Organization != nil {
		orgSlug := ""
		if resp.Organization.Slug != nil {
			orgSlug = *resp.Organization.Slug
		}
		orgUID := ""
		if resp.Organization.Uid != nil {
			orgUID = resp.Organization.Uid.String()
		}
		output.PrintMessage(os.Stdout, fmt.Sprintf("Organization:  %s (UID: %s)", orgSlug, orgUID))
	}
	output.PrintMessage(os.Stdout, "")
	output.PrintMessage(os.Stdout, "Authentication method: "+authMethodLabel(cliCtx.APIHelper.AuthMethod(ctx)))
	tokenPath, _ := config.TokenPath()
	output.PrintMessage(os.Stdout, "Token location: "+tokenPath)

	return nil
}

// authMethodLabel renders the human-readable label for an auth method.
func authMethodLabel(method string) string {
	if method == authMethodPAT {
		return "Personal Access Token (PAT)"
	}

	return "JWT token"
}

// authSwitchOrgAction handles the switch-org command.
func authSwitchOrgAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Get org from args
	org := cmd.Args().First()
	if org == "" {
		return cli.Exit("Error: organization name is required", 5)
	}

	// Get API client
	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	// Switch org
	resp, err := apiClient.SwitchOrgWithResponse(ctx, client.SwitchOrgJSONRequestBody{
		Org: org,
	})
	if err != nil {
		return cliCtx.HandleError("Failed to switch organization", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to switch organization", resp.StatusCode())
	}

	// Save new tokens
	if resp.JSON200.AccessToken != nil && resp.JSON200.RefreshToken != nil {
		if saveErr := cliCtx.APIHelper.SaveTokens(*resp.JSON200.AccessToken, *resp.JSON200.RefreshToken); saveErr != nil {
			return cliCtx.HandleError("Failed to save tokens", saveErr)
		}
	}

	// Output
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(map[string]interface{}{
			keySuccess: true,
			keyMessage: "Switched to organization: " + org,
		})
	}

	output.PrintSuccess(os.Stdout, "Switched to organization: "+org)
	return nil
}

// promptForInput prompts the user for input.
func promptForInput(prompt string) (string, error) {
	fmt.Print(prompt) //nolint:forbidigo // Interactive prompt requires direct stdin
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

// readPassword reads a password from stdin without echoing.
func readPassword(prompt string) (string, error) {
	fmt.Print(prompt) //nolint:forbidigo // Interactive prompt requires direct stdin
	bytePassword, err := term.ReadPassword(syscall.Stdin)
	fmt.Println() //nolint:forbidigo // Print newline after password input
	if err != nil {
		return "", err
	}
	return string(bytePassword), nil
}
