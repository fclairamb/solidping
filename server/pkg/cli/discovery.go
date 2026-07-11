package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// errDiscoveryTypeRequired is returned when `sp discovery scans create` is missing --type.
var errDiscoveryTypeRequired = errors.New("--type is required")

// discoveryTypesAction lists the registered discovery types.
func discoveryTypesAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListDiscoveryTypesWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list discovery types", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list discovery types", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No discovery types")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colType, "SOURCE"})

	for i := range *resp.JSON200.Data {
		dt := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{derefStr(dt.Type), derefStr(dt.Source)})
	}

	tbl.Render()

	return nil
}

// discoveryScansListAction lists discovery scans.
func discoveryScansListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListDiscoveryScansWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list scans", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list scans", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No scans found")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colType, colStatus})

	for i := range *resp.JSON200.Data {
		scan := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{derefStr(scan.Uid), derefStr(scan.Type), derefStr(scan.Status)})
	}

	tbl.Render()

	return nil
}

// discoveryScansCreateAction starts a new discovery scan.
func discoveryScansCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	scanType := cmd.String("type")
	if scanType == "" {
		return cli.Exit("Error: "+errDiscoveryTypeRequired.Error(), 5)
	}

	body := client.StartDiscoveryScanJSONRequestBody{Type: scanType}

	if paramsJSON := cmd.String("params"); paramsJSON != "" {
		var params map[string]any
		if jsonErr := json.Unmarshal([]byte(paramsJSON), &params); jsonErr != nil {
			return cli.Exit("Error: --params must be a JSON object: "+jsonErr.Error(), 5)
		}
		body.Parameters = &params
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.StartDiscoveryScanWithResponse(ctx, cliCtx.GetOrg(), body)
	if err != nil {
		return cliCtx.HandleError("Failed to start scan", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to start scan", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	uid := ""
	if resp.JSON201.Data != nil && resp.JSON201.Data.Uid != nil {
		uid = *resp.JSON201.Data.Uid
	}

	output.PrintSuccess(os.Stdout, "Started discovery scan "+uid)

	return nil
}

// discoveryScansGetAction fetches a single scan by UID.
func discoveryScansGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: scan UID is required", 5)
	}

	jobUID, err := uuid.Parse(cmd.Args().Get(0))
	if err != nil {
		return cli.Exit("Error: invalid scan UID: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetDiscoveryScanWithResponse(ctx, cliCtx.GetOrg(), jobUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get scan", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get scan", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if scan := resp.JSON200.Data; scan != nil {
		output.PrintMessage(os.Stdout, "UID:    "+derefStr(scan.Uid))
		output.PrintMessage(os.Stdout, "Type:   "+derefStr(scan.Type))
		output.PrintMessage(os.Stdout, "Status: "+derefStr(scan.Status))
	}

	return nil
}

// discoveryScansCancelAction cancels a running scan.
func discoveryScansCancelAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: scan UID is required", 5)
	}

	jobUID, err := uuid.Parse(cmd.Args().Get(0))
	if err != nil {
		return cli.Exit("Error: invalid scan UID: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CancelDiscoveryScanWithResponse(ctx, cliCtx.GetOrg(), jobUID)
	if err != nil {
		return cliCtx.HandleError("Failed to cancel scan", err)
	}

	if resp.StatusCode() != 200 && resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to cancel scan", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Cancelled scan "+jobUID.String())

	return nil
}

// discoveryChecksListAction lists discovered (suggested) checks.
//
//nolint:cyclop // CLI parameter parsing and table formatting
func discoveryChecksListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.ListDiscoveredChecksParams{}
	if jobUID := cmd.String("job-uid"); jobUID != "" {
		params.JobUid = &jobUID
	}
	if group := cmd.String("group"); group != "" {
		params.Group = &group
	}
	if source := cmd.String("source"); source != "" {
		params.Source = &source
	}
	if cmd.IsSet("promoted") {
		promoted := cmd.Bool("promoted")
		params.Promoted = &promoted
	}

	resp, err := apiClient.ListDiscoveredChecksWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list discovered checks", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list discovered checks", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No discovered checks")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colType, "GROUP", colName, "PROMOTED"})

	for i := range *resp.JSON200.Data {
		dc := &(*resp.JSON200.Data)[i]
		promoted := "no"
		if dc.PromotedToCheckUid != nil && *dc.PromotedToCheckUid != "" {
			promoted = "yes"
		}
		tbl.AppendRow(table.Row{
			derefStr(dc.Uid),
			derefStr(dc.Type),
			derefStr(dc.GroupLabel),
			derefStr(dc.Name),
			promoted,
		})
	}

	tbl.Render()

	return nil
}

// discoveryChecksPromoteAction promotes discovered checks into real checks.
func discoveryChecksPromoteAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: at least one discovered-check UID is required", 5)
	}

	body := client.PromoteDiscoveredChecksJSONRequestBody{
		Uids: cmd.Args().Slice(),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.PromoteDiscoveredChecksWithResponse(ctx, cliCtx.GetOrg(), body)
	if err != nil {
		return cliCtx.HandleError("Failed to promote checks", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to promote checks", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	created := 0
	if resp.JSON201.Data != nil {
		created = len(*resp.JSON201.Data)
	}

	output.PrintSuccess(os.Stdout, fmt.Sprintf("Promoted %d check(s)", created))

	return nil
}

// discoveryChecksDismissAction dismisses a discovered check.
func discoveryChecksDismissAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: discovered-check UID is required", 5)
	}

	uid := cmd.Args().Get(0)

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DismissDiscoveredCheckWithResponse(ctx, cliCtx.GetOrg(), uid)
	if err != nil {
		return cliCtx.HandleError("Failed to dismiss check", err)
	}

	if resp.StatusCode() != 200 && resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to dismiss check", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Dismissed discovered check "+uid)

	return nil
}
