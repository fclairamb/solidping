package cli

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// renderNotificationRoute prints a single route in text mode.
func renderNotificationRoute(route *client.NotificationRoute) {
	output.PrintMessage(os.Stdout, "UID:      "+route.Uid)
	output.PrintMessage(os.Stdout, "Enabled:  "+strconv.FormatBool(route.Enabled))
	output.PrintMessage(os.Stdout, "Position: "+strconv.Itoa(route.Position))
	output.PrintMessage(os.Stdout, "Contact:  "+route.Contact.Type+" = "+route.Contact.Value)

	if route.Contact.Label != "" {
		output.PrintMessage(os.Stdout, "Label:    "+route.Contact.Label)
	}
}

// notificationRoutesListAction lists the caller's notification routes.
func notificationRoutesListAction(ctx context.Context, cmd *cli.Command) error {
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
		return cliCtx.HandleError("Failed to list notification routes", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list notification routes", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No notification routes found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colEnabled, colPosition, colType, colValue, colLabel})

	for i := range *resp.JSON200.Data {
		route := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			route.Uid,
			strconv.FormatBool(route.Enabled),
			strconv.Itoa(route.Position),
			route.Contact.Type,
			route.Contact.Value,
			route.Contact.Label,
		})
	}

	tbl.Render()

	if resp.JSON200.SlackSuggestion != nil {
		output.PrintMessage(os.Stdout, "\nSlack DM available: add contact type slack_user with value "+
			safeStr(resp.JSON200.SlackSuggestion.SlackUserId))
	}

	return nil
}

// notificationRoutesUpdateAction toggles enabled and/or reorders the caller's routes.
func notificationRoutesUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	routeUID := cmd.Args().First()
	if routeUID == "" {
		return cli.Exit(msgRouteUIDRequired, 5)
	}

	body := client.UpdateMyNotificationRouteJSONRequestBody{}
	hasChange := false

	switch {
	case cmd.Bool(flagEnabled):
		enabled := true
		body.Enabled = &enabled
		hasChange = true
	case cmd.Bool(flagDisabled):
		disabled := false
		body.Enabled = &disabled
		hasChange = true
	}

	if cmd.IsSet(flagOrder) {
		order := splitCommaList(cmd.String(flagOrder))
		if len(order) > 0 {
			body.RouteUids = &order
			hasChange = true
		}
	}

	if !hasChange {
		return cli.Exit("Error: specify --enabled, --disabled, or --order", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateMyNotificationRouteWithResponse(ctx, cliCtx.GetOrg(), routeUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to update notification route", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update notification route", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Notification route updated: "+routeUID)
	renderNotificationRoute(resp.JSON200)

	return nil
}

// notificationRoutesTestAction sends a test notification through a route.
func notificationRoutesTestAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	routeUID := cmd.Args().First()
	if routeUID == "" {
		return cli.Exit(msgRouteUIDRequired, 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.TestMyNotificationRouteWithResponse(ctx, cliCtx.GetOrg(), routeUID)
	if err != nil {
		return cliCtx.HandleError("Failed to send test notification", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to send test notification", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Success("Test notification dispatched: " + routeUID)
	}

	output.PrintSuccess(os.Stdout, "Test notification dispatched: "+routeUID)

	return nil
}

// splitCommaList splits a comma-separated string into a trimmed, non-empty slice.
func splitCommaList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))

	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}

	return out
}
