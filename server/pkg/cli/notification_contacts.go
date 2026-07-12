package cli

import (
	"context"
	"os"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// notificationContactsListAction lists the caller's notification contacts. The
// contacts are read from the routes list (each route wraps one contact).
func notificationContactsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListMyNotificationRoutesWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list notification contacts", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list notification contacts", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No notification contacts found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colType, colValue, colLabel, "VERIFIED"})

	for i := range *resp.JSON200.Data {
		contact := &(*resp.JSON200.Data)[i].Contact
		tbl.AppendRow(table.Row{
			contact.Uid,
			contact.Type,
			contact.Value,
			contact.Label,
			safeTime(contact.VerifiedAt),
		})
	}

	tbl.Render()

	return nil
}

// notificationContactsAddAction adds a new notification contact (and its route).
func notificationContactsAddAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	contactType := cmd.String(flagType)
	if contactType == "" {
		return cli.Exit("Error: --type is required", 5)
	}

	value := cmd.String(flagValue)
	if value == "" {
		return cli.Exit("Error: --value is required", 5)
	}

	body := client.AddMyNotificationContactJSONRequestBody{
		Type:  contactType,
		Value: value,
		Label: optString(cmd, flagLabel),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.AddMyNotificationContactWithResponse(ctx, cliCtx.GetOrg(), body)
	if err != nil {
		return cliCtx.HandleError("Failed to add notification contact", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to add notification contact", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Notification contact added")
	renderNotificationRoute(resp.JSON201)

	return nil
}

// notificationContactsRemoveAction deletes a notification contact by UID.
func notificationContactsRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	contactUID := cmd.Args().First()
	if contactUID == "" {
		return cli.Exit(msgContactUIDRequired, 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.RemoveMyNotificationContactWithResponse(ctx, cliCtx.GetOrg(), contactUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove notification contact", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove notification contact", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Success("Notification contact removed: " + contactUID)
	}

	output.PrintSuccess(os.Stdout, "Notification contact removed: "+contactUID)

	return nil
}
