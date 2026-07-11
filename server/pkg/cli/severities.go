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

// Severity CLI shared strings.
const (
	colChannels = "CHANNELS"

	flagChannels = "channels"

	msgSeverityRequired = "Error: severity uid or slug is required"
)

// severityArg reads the severity uid-or-slug from the first argument.
func severityArg(cmd *cli.Command) (string, error) {
	identifier := cmd.Args().First()
	if identifier == "" {
		return "", cli.Exit(msgSeverityRequired, 5)
	}

	return identifier, nil
}

// optStringSlice returns a pointer to the string-slice flag when set, else nil.
func optStringSlice(cmd *cli.Command, name string) *[]string {
	if !cmd.IsSet(name) {
		return nil
	}

	value := cmd.StringSlice(name)

	return &value
}

// renderSeverity prints a single severity in text mode.
func renderSeverity(severity *client.Severity) {
	output.PrintMessage(os.Stdout, "UID:         "+severity.Uid.String())
	output.PrintMessage(os.Stdout, "Slug:        "+severity.Slug)
	output.PrintMessage(os.Stdout, "Name:        "+severity.Name)
	output.PrintMessage(os.Stdout, "Default:     "+strconv.FormatBool(severity.IsDefault))
	output.PrintMessage(os.Stdout, "Channels:    "+strings.Join(severity.Channels, ", "))

	if severity.Description != nil && *severity.Description != "" {
		output.PrintMessage(os.Stdout, "Description: "+*severity.Description)
	}
}

// severitiesListAction lists all severities for the org.
func severitiesListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListSeveritiesWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list severities", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list severities", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No severities found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colSlug, colName, colDefault, colChannels})

	for i := range *resp.JSON200.Data {
		severity := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			severity.Uid.String(),
			severity.Slug,
			severity.Name,
			strconv.FormatBool(severity.IsDefault),
			strings.Join(severity.Channels, ", "),
		})
	}

	tbl.Render()

	return nil
}

// severitiesCreateAction creates a new severity.
func severitiesCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	name := cmd.String(flagName)
	if name == "" {
		return cli.Exit("Error: --name is required", 5)
	}

	body := client.CreateSeverityJSONRequestBody{
		Name:        name,
		Slug:        optString(cmd, keySlug),
		Description: optString(cmd, flagDescription),
		Channels:    optStringSlice(cmd, flagChannels),
	}

	if cmd.Bool(flagDefault) {
		isDefault := true
		body.IsDefault = &isDefault
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateSeverityWithResponse(ctx, cliCtx.GetOrg(), body)
	if err != nil {
		return cliCtx.HandleError("Failed to create severity", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create severity", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Created severity "+resp.JSON201.Uid.String())
	renderSeverity(resp.JSON201)

	return nil
}

// severitiesGetAction fetches a single severity by uid or slug.
func severitiesGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	identifier, err := severityArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetSeverityWithResponse(ctx, cliCtx.GetOrg(), identifier)
	if err != nil {
		return cliCtx.HandleError("Failed to get severity", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get severity", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderSeverity(resp.JSON200)

	return nil
}

// severitiesUpdateAction patches a severity.
func severitiesUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	identifier, err := severityArg(cmd)
	if err != nil {
		return err
	}

	body := client.UpdateSeverityJSONRequestBody{
		Name:        optString(cmd, flagName),
		Slug:        optString(cmd, keySlug),
		Description: optString(cmd, flagDescription),
		Channels:    optStringSlice(cmd, flagChannels),
		IsDefault:   boolPair(cmd, flagDefault, flagNoDefault),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateSeverityWithResponse(ctx, cliCtx.GetOrg(), identifier, body)
	if err != nil {
		return cliCtx.HandleError("Failed to update severity", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update severity", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Updated severity "+resp.JSON200.Uid.String())
	renderSeverity(resp.JSON200)

	return nil
}

// severitiesRemoveAction deletes a severity by uid or slug.
func severitiesRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	identifier, err := severityArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteSeverityWithResponse(ctx, cliCtx.GetOrg(), identifier)
	if err != nil {
		return cliCtx.HandleError("Failed to remove severity", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove severity", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Removed severity "+identifier)

	return nil
}
