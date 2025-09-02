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

// authLoginAction handles the login command.
func authLoginAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Get email and password from flags or prompt
	email := cmd.String("email")
	password := cmd.String("password")

	if email == "" {
		email, err = promptForInput("Email: ")
		if err != nil {
			return err
		}
	}

	if password == "" {
		password, err = readPassword("Password: ")
		if err != nil {
			return err
		}
	}

	// Perform login
	_, user, err := cliCtx.APIHelper.Login(ctx, cliCtx.GetOrg(), email, password)
	if err != nil {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(fmt.Errorf("login failed: %w", err))
		}
		output.PrintError(os.Stdout, fmt.Sprintf("Login failed: %v", err))
		return cli.Exit("", 3) // Authentication error
	}

	// Output success
	if !cliCtx.IsText() { //nolint:nestif // JSON vs text output handling
		tokenPath, _ := config.TokenPath()
		userMap := map[string]interface{}{}
		if user != nil {
			if user.Uid != nil {
				userMap["uid"] = user.Uid.String()
			}
			if user.Email != nil {
				userMap["email"] = *user.Email
			}
		}
		return cliCtx.Outputter.Print(map[string]interface{}{
			"success":    true,
			"user":       userMap,
			"token_path": tokenPath,
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
			"success": true,
			"message": "Logged out successfully",
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
				orgMap["slug"] = *resp.Organization.Slug
			}
		}
		return cliCtx.Outputter.Print(map[string]interface{}{
			"user":         userMap,
			"organization": orgMap,
			"auth_method":  "jwt",
			"token_source": tokenPath,
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
	output.PrintMessage(os.Stdout, "Authentication method: JWT token")
	tokenPath, _ := config.TokenPath()
	output.PrintMessage(os.Stdout, "Token location: "+tokenPath)

	return nil
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
			"success": true,
			"message": "Switched to organization: " + org,
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
