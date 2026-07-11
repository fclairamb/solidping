package cli

import (
	"context"
	"os"
	"strconv"

	"github.com/jedib0t/go-pretty/v6/table"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Self-service auth CLI shared strings.
const (
	flagPassword = "password"

	msgTokenArgRequired = "Error: token is required"
)

// publicAPIClient builds an unauthenticated API client for the self-service
// auth endpoints (register / confirm / reset / providers / invite), which are
// reachable without a session token.
func publicAPIClient(cliCtx *Context) (*client.SolidPingClient, error) {
	return client.New(client.Config{
		BaseURL: cliCtx.Config.URL,
		Verbose: cliCtx.Verbose,
	})
}

// saveAuthSession persists the tokens from a login-shaped response so the flows
// that authenticate the user (confirm-registration, accept-invite) leave the
// CLI logged in, mirroring `sp auth login`.
func saveAuthSession(cliCtx *Context, resp *client.LoginResponse) error {
	if resp.AccessToken == nil || resp.RefreshToken == nil {
		return nil
	}

	if err := cliCtx.APIHelper.SaveTokens(*resp.AccessToken, *resp.RefreshToken); err != nil {
		return cliCtx.HandleError("Failed to save tokens", err)
	}

	return nil
}

// loginResponseEmail safely reads the user email off a login-shaped response.
func loginResponseEmail(resp *client.LoginResponse) string {
	if resp != nil && resp.User != nil && resp.User.Email != nil {
		return string(*resp.User.Email)
	}

	return ""
}

// authRegisterAction handles `sp auth register` (self-service registration).
func authRegisterAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	email := cmd.String(flagEmail)
	if email == "" {
		if email, err = promptForInput("Email: "); err != nil {
			return err
		}
	}

	password := cmd.String(flagPassword)
	if password == "" {
		if password, err = readPassword("Password: "); err != nil {
			return err
		}
	}

	body := client.RegisterJSONRequestBody{
		Email:    openapi_types.Email(email),
		Password: password,
	}
	if name := cmd.String(flagName); name != "" {
		body.Name = &name
	}

	apiClient, err := publicAPIClient(cliCtx)
	if err != nil {
		return cliCtx.HandleError("Failed to create client", err)
	}

	resp, err := apiClient.RegisterWithResponse(ctx, body)
	if err != nil {
		return cliCtx.HandleError("Failed to register", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to register", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, derefMessage(resp.JSON200.Message, "Registration submitted"))

	return nil
}

// authConfirmRegistrationAction handles `sp auth confirm-registration <token>`.
func authConfirmRegistrationAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	token := cmd.Args().First()
	if token == "" {
		return cli.Exit(msgTokenArgRequired, 5)
	}

	apiClient, err := publicAPIClient(cliCtx)
	if err != nil {
		return cliCtx.HandleError("Failed to create client", err)
	}

	resp, err := apiClient.ConfirmRegistrationWithResponse(ctx, client.ConfirmRegistrationJSONRequestBody{
		Token: token,
	})
	if err != nil {
		return cliCtx.HandleError("Failed to confirm registration", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to confirm registration", resp.StatusCode())
	}

	if saveErr := saveAuthSession(cliCtx, resp.JSON200); saveErr != nil {
		return saveErr
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Registration confirmed")
	if email := loginResponseEmail(resp.JSON200); email != "" {
		output.PrintMessage(os.Stdout, "Logged in as: "+email)
	}

	return nil
}

// authRequestPasswordResetAction handles `sp auth request-password-reset`.
func authRequestPasswordResetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	email := cmd.String(flagEmail)
	if email == "" {
		if email, err = promptForInput("Email: "); err != nil {
			return err
		}
	}

	apiClient, err := publicAPIClient(cliCtx)
	if err != nil {
		return cliCtx.HandleError("Failed to create client", err)
	}

	resp, err := apiClient.RequestPasswordResetWithResponse(ctx, client.RequestPasswordResetJSONRequestBody{
		Email: openapi_types.Email(email),
	})
	if err != nil {
		return cliCtx.HandleError("Failed to request password reset", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to request password reset", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout,
		derefMessage(resp.JSON200.Message, "If an account exists, a reset link has been sent"))

	return nil
}

// authResetPasswordAction handles `sp auth reset-password`.
func authResetPasswordAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	token := cmd.String(flagToken)
	if token == "" {
		return cli.Exit(msgTokenArgRequired, 5)
	}

	password := cmd.String(flagPassword)
	if password == "" {
		if password, err = readPassword("New password: "); err != nil {
			return err
		}
	}

	apiClient, err := publicAPIClient(cliCtx)
	if err != nil {
		return cliCtx.HandleError("Failed to create client", err)
	}

	resp, err := apiClient.ResetPasswordWithResponse(ctx, client.ResetPasswordJSONRequestBody{
		Token:    token,
		Password: password,
	})
	if err != nil {
		return cliCtx.HandleError("Failed to reset password", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to reset password", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, derefMessage(resp.JSON200.Message, "Password reset"))

	return nil
}

// authUpdateAction handles `sp auth update` (PATCH /auth/me, authenticated).
func authUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if !cmd.IsSet(flagName) {
		return cli.Exit("Error: --name is required", 5)
	}

	name := cmd.String(flagName)

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateMeWithResponse(ctx, client.UpdateMeJSONRequestBody{
		Name: &name,
	})
	if err != nil {
		return cliCtx.HandleError("Failed to update profile", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update profile", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Profile updated")
	if resp.JSON200.User != nil && resp.JSON200.User.Name != nil {
		output.PrintMessage(os.Stdout, "Name: "+*resp.JSON200.User.Name)
	}

	return nil
}

// authProvidersAction handles `sp auth providers` (configured auth providers).
func authProvidersAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := publicAPIClient(cliCtx)
	if err != nil {
		return cliCtx.HandleError("Failed to create client", err)
	}

	resp, err := apiClient.ListAuthProvidersWithResponse(ctx)
	if err != nil {
		return cliCtx.HandleError("Failed to list auth providers", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list auth providers", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderAuthProviders(resp.JSON200)

	return nil
}

// renderAuthProviders prints the providers list plus registration/passkey flags.
func renderAuthProviders(resp *client.AuthProvidersResponse) {
	if resp.Data == nil || len(*resp.Data) == 0 {
		output.PrintMessage(os.Stdout, "No SSO/OAuth providers configured")
	} else {
		tbl := output.NewTable(os.Stdout)
		tbl.AppendHeader(table.Row{colName, colType})

		for i := range *resp.Data {
			provider := &(*resp.Data)[i]
			tbl.AppendRow(table.Row{
				derefString(provider.Name),
				derefString(provider.Type),
			})
		}

		tbl.Render()
	}

	output.PrintMessage(os.Stdout, "Registration enabled: "+strconv.FormatBool(derefBool(resp.RegistrationEnabled)))
	output.PrintMessage(os.Stdout, "Passkeys enabled:     "+strconv.FormatBool(derefBool(resp.PasskeysEnabled)))
}

// authInviteAction handles `sp auth invite <token>` (inspect an invitation).
func authInviteAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	token := cmd.Args().First()
	if token == "" {
		return cli.Exit(msgTokenArgRequired, 5)
	}

	apiClient, err := publicAPIClient(cliCtx)
	if err != nil {
		return cliCtx.HandleError("Failed to create client", err)
	}

	resp, err := apiClient.GetInviteWithResponse(ctx, token)
	if err != nil {
		return cliCtx.HandleError("Failed to get invitation", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get invitation", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	info := resp.JSON200
	output.PrintMessage(os.Stdout, "Organization: "+derefString(info.OrgName)+" ("+derefString(info.OrgSlug)+")")
	if info.Role != nil {
		output.PrintMessage(os.Stdout, "Role:         "+string(*info.Role))
	}
	output.PrintMessage(os.Stdout, "Email:        "+derefString(info.Email))

	return nil
}

// authAcceptInviteAction handles `sp auth accept-invite`.
func authAcceptInviteAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	token := cmd.String(flagToken)
	if token == "" {
		return cli.Exit(msgTokenArgRequired, 5)
	}

	body := client.AcceptInviteJSONRequestBody{Token: token}
	if cmd.IsSet(flagName) {
		name := cmd.String(flagName)
		body.Name = &name
	}
	if password := cmd.String(flagPassword); password != "" {
		body.Password = &password
	}

	apiClient, err := publicAPIClient(cliCtx)
	if err != nil {
		return cliCtx.HandleError("Failed to create client", err)
	}

	resp, err := apiClient.AcceptInviteWithResponse(ctx, body)
	if err != nil {
		return cliCtx.HandleError("Failed to accept invitation", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to accept invitation", resp.StatusCode())
	}

	if saveErr := saveAuthSession(cliCtx, resp.JSON200); saveErr != nil {
		return saveErr
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Invitation accepted")
	if email := loginResponseEmail(resp.JSON200); email != "" {
		output.PrintMessage(os.Stdout, "Logged in as: "+email)
	}

	return nil
}

// derefString returns the pointed-to string or "".
func derefString(s *string) string {
	if s != nil {
		return *s
	}

	return ""
}

// derefBool returns the pointed-to bool or false.
func derefBool(b *bool) bool {
	return b != nil && *b
}

// derefMessage returns the pointed-to message or a fallback.
func derefMessage(msg *string, fallback string) string {
	if msg != nil && *msg != "" {
		return *msg
	}

	return fallback
}
