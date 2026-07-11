package cli

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Invitation CLI shared strings.
const (
	flagEmail     = "email"
	flagRole      = "role"
	flagExpiresIn = "expires-in"
	flagApp       = "app"

	colRole    = "ROLE"
	colExpires = "EXPIRES"

	usageInviteRole = "Member role granted on acceptance: admin, user, viewer (default user)"
	usageExpiresIn  = "Invitation lifetime: 1h, 6h, 12h, 24h, 48h, 1w (default 24h)"
	usageInviteApp  = "Target dashboard for the invite link: dash0, dash (default dash0)"

	msgInvitationUIDRequired = "Error: invitation uid is required"
)

// invitationsCommand builds the "invitations" command group.
func invitationsCommand() *cli.Command {
	return &cli.Command{
		Name:    "invitations",
		Aliases: []string{"invitation", "invite"},
		Usage:   "Manage organization invitations",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:   flagList,
				Usage:  "List pending invitations",
				Action: invitationsListAction,
			},
			{
				Name:  cmdCreate,
				Usage: "Create an invitation",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagEmail, Usage: "Invitee email address", Required: true},
					&cli.StringFlag{Name: flagRole, Usage: usageInviteRole},
					&cli.StringFlag{Name: flagExpiresIn, Usage: usageExpiresIn},
					&cli.StringFlag{Name: flagApp, Usage: usageInviteApp},
				},
				Action: invitationsCreateAction,
			},
			{
				Name:      flagRemove,
				Aliases:   []string{"rm", cmdDelete, "revoke"},
				Usage:     "Revoke a pending invitation",
				ArgsUsage: argUID,
				Action:    invitationsRemoveAction,
			},
		},
	}
}

// invitationsListAction lists pending invitations for the org.
func invitationsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListInvitationsWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list invitations", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list invitations", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No pending invitations")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colEmail, colRole, colExpires, colCreated})

	for i := range *resp.JSON200.Data {
		invite := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			invite.Uid.String(),
			string(invite.Email),
			invite.Role,
			safeTime(invite.ExpiresAt),
			invite.CreatedAt.Format(time.RFC3339),
		})
	}

	tbl.Render()

	return nil
}

// invitationsCreateAction creates an invitation for the org.
func invitationsCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	body := client.CreateInvitationJSONRequestBody{
		Email: openapi_types.Email(cmd.String(flagEmail)),
	}

	if cmd.IsSet(flagRole) {
		role := client.CreateInvitationRequestRole(cmd.String(flagRole))
		body.Role = &role
	}

	if cmd.IsSet(flagExpiresIn) {
		expiresIn := client.CreateInvitationRequestExpiresIn(cmd.String(flagExpiresIn))
		body.ExpiresIn = &expiresIn
	}

	if cmd.IsSet(flagApp) {
		app := client.CreateInvitationRequestApp(cmd.String(flagApp))
		body.App = &app
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateInvitationWithResponse(ctx, cliCtx.GetOrg(), body)
	if err != nil {
		return cliCtx.HandleError("Failed to create invitation", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create invitation", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	invite := resp.JSON201
	output.PrintSuccess(os.Stdout, "Invitation created for "+string(invite.Email))
	output.PrintMessage(os.Stdout, "  UID:   "+invite.Uid.String())
	output.PrintMessage(os.Stdout, "  Role:  "+invite.Role)
	output.PrintMessage(os.Stdout, "  URL:   "+invite.InviteUrl)
	output.PrintMessage(os.Stdout, "  Expires: "+invite.ExpiresAt.Format(time.RFC3339))

	return nil
}

// invitationsRemoveAction revokes a pending invitation by UID.
func invitationsRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	raw := cmd.Args().First()
	if raw == "" {
		return cli.Exit(msgInvitationUIDRequired, 5)
	}

	inviteUID, err := uuid.Parse(raw)
	if err != nil {
		return cli.Exit("Error: invalid invitation uid: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteInvitationWithResponse(ctx, cliCtx.GetOrg(), inviteUID)
	if err != nil {
		return cliCtx.HandleError("Failed to revoke invitation", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to revoke invitation", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Invitation revoked: "+inviteUID.String())

	return nil
}
