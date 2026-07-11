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

// Taxonomy CLI shared strings (labels, regions, check types).
const (
	colValue       = "VALUE"
	colCount       = "COUNT"
	colEmoji       = "EMOJI"
	colDescription = "DESCRIPTION"
	colPeriodSecs  = "PERIOD(s)"

	flagKey        = "key"
	flagLabelQuery = "q"
)

// labelsListAction lists distinct label keys, or values for a given --key.
func labelsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.ListLabelsParams{
		Key:   optString(cmd, flagKey),
		Q:     optString(cmd, flagLabelQuery),
		Limit: optInt(cmd, flagLimit),
	}

	resp, err := apiClient.ListLabelsWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list labels", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list labels", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No labels found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colValue, colCount})

	for i := range *resp.JSON200.Data {
		suggestion := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{suggestion.Value, strconv.Itoa(suggestion.Count)})
	}

	tbl.Render()

	return nil
}

// regionsListAction lists the regions available to the org and its defaults.
func regionsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListRegionsWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list regions", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list regions", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No regions found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colSlug, colEmoji, colName})

	for i := range *resp.JSON200.Data {
		region := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{region.Slug, region.Emoji, region.Name})
	}

	tbl.Render()

	if resp.JSON200.DefaultRegions != nil && len(*resp.JSON200.DefaultRegions) > 0 {
		output.PrintMessage(os.Stdout, "Default regions: "+strings.Join(*resp.JSON200.DefaultRegions, ", "))
	}

	return nil
}

// checkTypesListAction lists the check type catalog with activation status.
func checkTypesListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListCheckTypesWithResponse(ctx)
	if err != nil {
		return cliCtx.HandleError("Failed to list check types", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list check types", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No check types found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colType, colEnabled, colDescription})

	for i := range *resp.JSON200.Data {
		checkType := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			checkType.Type,
			strconv.FormatBool(checkType.Enabled),
			checkType.Description,
		})
	}

	tbl.Render()

	return nil
}

// checkTypesSamplesAction lists sample configurations grouped by check type.
func checkTypesSamplesAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.ListCheckTypeSamplesParams{Type: optString(cmd, flagType)}

	resp, err := apiClient.ListCheckTypeSamplesWithResponse(ctx, params)
	if err != nil {
		return cliCtx.HandleError("Failed to list check type samples", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list check type samples", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No samples found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colType, colName, colSlug, colPeriodSecs})

	for i := range *resp.JSON200.Data {
		group := &(*resp.JSON200.Data)[i]
		for j := range group.Samples {
			sample := &group.Samples[j]
			tbl.AppendRow(table.Row{
				group.CheckType,
				sample.Name,
				sample.Slug,
				strconv.Itoa(sample.PeriodSeconds),
			})
		}
	}

	tbl.Render()

	return nil
}
