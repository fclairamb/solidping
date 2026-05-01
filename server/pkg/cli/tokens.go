package cli

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// tokensListAction handles listing personal access tokens.
func tokensListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	// List tokens for current org or all orgs
	if cmd.Bool("all") {
		return tokensListAll(ctx, cliCtx, apiClient)
	}

	tokenType := "personal_access_token"
	params := &client.ListOrgTokensParams{
		Type: &tokenType,
	}

	resp, err := apiClient.ListOrgTokensWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list tokens", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list tokens", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No tokens found")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colName, colCreated, "LAST USED", "EXPIRES"})

	for i := range *resp.JSON200.Data {
		token := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			safeUUID(token.Uid),
			safeStr(token.Name),
			safeTime(token.CreatedAt),
			safeTimeOrNever(token.LastUsedAt),
			safeTimeOrNever(token.ExpiresAt),
		})
	}

	tbl.Render()
	return nil
}

// tokensListAll lists tokens across all organizations.
func tokensListAll(ctx context.Context, cliCtx *Context, apiClient *client.SolidPingClient) error {
	tokenType := "personal_access_token"
	params := &client.ListAllTokensParams{
		Type: &tokenType,
	}

	resp, err := apiClient.ListAllTokensWithResponse(ctx, params)
	if err != nil {
		return cliCtx.HandleError("Failed to list tokens", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list tokens", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No tokens found")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{"UID", "NAME", "ORG", "CREATED", "LAST USED", "EXPIRES"})

	for i := range *resp.JSON200.Data {
		token := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			safeUUID(token.Uid),
			safeStr(token.Name),
			safeStr(token.OrgSlug),
			safeTime(token.CreatedAt),
			safeTimeOrNever(token.LastUsedAt),
			safeTimeOrNever(token.ExpiresAt),
		})
	}

	tbl.Render()
	return nil
}

// tokensCreateAction handles creating a personal access token.
//
//nolint:cyclop // CLI parameter parsing
func tokensCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	name := cmd.String("name")
	if name == "" {
		return cli.Exit("Error: --name is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	req := client.CreateTokenJSONRequestBody{
		Name: name,
	}

	// Parse expiration
	if expires := cmd.String("expires"); expires != "" && expires != "never" {
		var duration time.Duration
		switch expires {
		case "7d":
			duration = 7 * 24 * time.Hour
		case "30d":
			duration = 30 * 24 * time.Hour
		case "90d":
			duration = 90 * 24 * time.Hour
		case "1y":
			duration = 365 * 24 * time.Hour
		default:
			return cli.Exit("Error: --expires must be one of: 7d, 30d, 90d, 1y, never", 5)
		}
		expiresAt := time.Now().Add(duration)
		req.ExpiresAt = &expiresAt
	}

	resp, err := apiClient.CreateTokenWithResponse(ctx, cliCtx.GetOrg(), req)
	if err != nil {
		return cliCtx.HandleError("Failed to create token", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create token", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Token created successfully!")
	if resp.JSON201.Token != nil {
		output.PrintMessage(os.Stdout, "  Token: "+*resp.JSON201.Token)
	}
	if resp.JSON201.Name != nil {
		output.PrintMessage(os.Stdout, "  Name:  "+*resp.JSON201.Name)
	}
	output.PrintMessage(os.Stdout, "  Expires: "+safeTimeOrNever(resp.JSON201.ExpiresAt))
	output.PrintWarning(os.Stdout, "Save this token — it won't be shown again")

	return nil
}

// tokensRevokeAction handles revoking a token.
func tokensRevokeAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	uidStr := cmd.Args().First()
	if uidStr == "" {
		return cli.Exit("Error: token UID is required", 5)
	}

	tokenUID, err := uuid.Parse(uidStr)
	if err != nil {
		return cli.Exit("Error: invalid token UID: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.RevokeTokenWithResponse(ctx, tokenUID)
	if err != nil {
		return cliCtx.HandleError("Failed to revoke token", err)
	}

	if resp.StatusCode() != 200 && resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to revoke token", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Success("Token revoked successfully: " + uidStr)
	}

	output.PrintSuccess(os.Stdout, "Token revoked successfully: "+uidStr)
	return nil
}

// safeStr returns the string value or empty string if nil.
func safeStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// safeTime formats a time pointer as RFC3339 or empty string.
func safeTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// safeTimeOrNever formats a time pointer as RFC3339 or "Never".
func safeTimeOrNever(t *time.Time) string {
	if t == nil {
		return "Never"
	}
	return t.Format(time.RFC3339)
}
