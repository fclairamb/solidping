package cli

import (
	"context"
	"os"
	"strconv"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Check-group CLI shared strings.
const (
	colSort   = "SORT"
	colChecks = "CHECKS"

	flagSortOrder = "sort-order"

	msgCheckGroupRequired = "Error: check group uid or slug is required"
)

// checkGroupArg reads the check-group uid-or-slug from the first argument.
func checkGroupArg(cmd *cli.Command) (string, error) {
	identifier := cmd.Args().First()
	if identifier == "" {
		return "", cli.Exit(msgCheckGroupRequired, 5)
	}

	return identifier, nil
}

// renderCheckGroup prints a single check group in text mode.
func renderCheckGroup(group *client.CheckGroup) {
	output.PrintMessage(os.Stdout, "UID:         "+group.Uid.String())
	output.PrintMessage(os.Stdout, "Name:        "+group.Name)
	output.PrintMessage(os.Stdout, "Slug:        "+group.Slug)
	output.PrintMessage(os.Stdout, "Sort order:  "+strconv.Itoa(group.SortOrder))
	output.PrintMessage(os.Stdout, "Checks:      "+strconv.Itoa(group.CheckCount))

	if group.Description != nil && *group.Description != "" {
		output.PrintMessage(os.Stdout, "Description: "+*group.Description)
	}
}

// checkGroupsListAction lists all check groups for the org.
func checkGroupsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListCheckGroupsWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list check groups", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list check groups", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No check groups found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colName, colSlug, colSort, colChecks})

	for i := range *resp.JSON200.Data {
		group := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			group.Uid.String(),
			group.Name,
			group.Slug,
			strconv.Itoa(group.SortOrder),
			strconv.Itoa(group.CheckCount),
		})
	}

	tbl.Render()

	return nil
}

// checkGroupsCreateAction creates a new check group.
func checkGroupsCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	name := cmd.String(flagName)
	if name == "" {
		return cli.Exit("Error: --name is required", 5)
	}

	body := client.CreateCheckGroupJSONRequestBody{
		Name:        name,
		Slug:        optString(cmd, keySlug),
		Description: optString(cmd, flagDescription),
		SortOrder:   optInt(cmd, flagSortOrder),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateCheckGroupWithResponse(ctx, cliCtx.GetOrg(), body)
	if err != nil {
		return cliCtx.HandleError("Failed to create check group", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create check group", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Created check group "+resp.JSON201.Uid.String())
	renderCheckGroup(resp.JSON201)

	return nil
}

// checkGroupsGetAction fetches a single check group by uid or slug.
func checkGroupsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	identifier, err := checkGroupArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetCheckGroupWithResponse(ctx, cliCtx.GetOrg(), identifier)
	if err != nil {
		return cliCtx.HandleError("Failed to get check group", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get check group", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderCheckGroup(resp.JSON200)

	return nil
}

// checkGroupsUpdateAction patches a check group.
func checkGroupsUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	identifier, err := checkGroupArg(cmd)
	if err != nil {
		return err
	}

	body := client.UpdateCheckGroupJSONRequestBody{
		Name:        optString(cmd, flagName),
		Slug:        optString(cmd, keySlug),
		Description: optString(cmd, flagDescription),
		SortOrder:   optInt(cmd, flagSortOrder),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateCheckGroupWithResponse(ctx, cliCtx.GetOrg(), identifier, body)
	if err != nil {
		return cliCtx.HandleError("Failed to update check group", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update check group", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Updated check group "+resp.JSON200.Uid.String())
	renderCheckGroup(resp.JSON200)

	return nil
}

// checkGroupsRemoveAction deletes a check group by uid or slug.
func checkGroupsRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	identifier, err := checkGroupArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteCheckGroupWithResponse(ctx, cliCtx.GetOrg(), identifier)
	if err != nil {
		return cliCtx.HandleError("Failed to remove check group", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove check group", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Removed check group "+identifier)

	return nil
}
