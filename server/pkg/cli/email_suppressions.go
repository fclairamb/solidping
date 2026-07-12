package cli

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
)

// Email-suppression CLI shared strings.
const (
	msgSuppressionUIDRequired = "Error: email suppression uid is required"
)

// emailSuppressionsCommand builds the "email-suppressions" command group.
func emailSuppressionsCommand() *cli.Command {
	return &cli.Command{
		Name:    "email-suppressions",
		Aliases: []string{"email-suppression", "suppressions"},
		Usage:   "Manage the per-recipient email suppression list",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:   flagList,
				Usage:  "List email suppressions",
				Action: emailSuppressionsListAction,
			},
			{
				Name:      flagRemove,
				Aliases:   []string{"rm", cmdDelete},
				Usage:     "Remove an email suppression (re-subscribe the recipient)",
				ArgsUsage: argUID,
				Action:    emailSuppressionsRemoveAction,
			},
		},
	}
}

// emailSuppressionsListAction lists suppressions for the org.
func emailSuppressionsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListEmailSuppressionsWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list email suppressions", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list email suppressions", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No email suppressions")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colEmail, colCheck, colSource, colCreated})

	for i := range *resp.JSON200.Data {
		sup := &(*resp.JSON200.Data)[i]
		check := derefStr(sup.CheckName)
		if check == "" {
			check = derefStr(sup.CheckUid)
		}
		if check == "" {
			check = "(all checks)"
		}

		tbl.AppendRow(table.Row{
			sup.Uid.String(),
			sup.Email,
			check,
			sup.Source,
			sup.CreatedAt.Format(time.RFC3339),
		})
	}

	tbl.Render()

	return nil
}

// emailSuppressionsRemoveAction deletes a suppression by UID (re-subscribe).
func emailSuppressionsRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	raw := cmd.Args().First()
	if raw == "" {
		return cli.Exit(msgSuppressionUIDRequired, 5)
	}

	supUID, err := uuid.Parse(raw)
	if err != nil {
		return cli.Exit("Error: invalid email suppression uid: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteEmailSuppressionWithResponse(ctx, cliCtx.GetOrg(), supUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove email suppression", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove email suppression", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Email suppression removed: "+supUID.String())

	return nil
}
