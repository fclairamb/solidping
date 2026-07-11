package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

const (
	boolYes = "yes"
	boolNo  = "no"
)

// systemListAction handles listing system parameters.
func systemListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListSystemParametersWithResponse(ctx)
	if err != nil {
		return cliCtx.HandleError("Failed to list system parameters", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list system parameters", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No system parameters found")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{"KEY", "VALUE", "SECRET"})

	for i := range *resp.JSON200.Data {
		param := &(*resp.JSON200.Data)[i]
		secret := boolNo
		if param.Secret != nil && *param.Secret {
			secret = boolYes
		}
		value := ""
		if param.Value != nil {
			value = fmt.Sprintf("%v", param.Value)
		}
		tbl.AppendRow(table.Row{
			safeStr(param.Key),
			value,
			secret,
		})
	}

	tbl.Render()
	return nil
}

// systemGetAction handles getting a system parameter.
func systemGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	key := cmd.Args().First()
	if key == "" {
		return cli.Exit("Error: parameter key is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetSystemParameterWithResponse(ctx, key)
	if err != nil {
		return cliCtx.HandleError("Failed to get system parameter", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get system parameter", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	param := resp.JSON200
	output.PrintMessage(os.Stdout, "Key:    "+safeStr(param.Key))
	output.PrintMessage(os.Stdout, fmt.Sprintf("Value:  %v", param.Value))
	secret := boolNo
	if param.Secret != nil && *param.Secret {
		secret = boolYes
	}
	output.PrintMessage(os.Stdout, "Secret: "+secret)

	return nil
}

// systemSetAction handles setting a system parameter.
func systemSetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 2 {
		return cli.Exit("Error: key and value are required", 5)
	}

	key := cmd.Args().Get(0)
	value := cmd.Args().Get(1)

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	req := client.SetSystemParameterJSONRequestBody{
		Value: value,
	}

	if cmd.IsSet("secret") {
		secret := cmd.Bool("secret")
		req.Secret = &secret
	}

	resp, err := apiClient.SetSystemParameterWithResponse(ctx, key, req)
	if err != nil {
		return cliCtx.HandleError("Failed to set system parameter", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to set system parameter", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Parameter set successfully: "+key)
	return nil
}

// systemDeleteAction handles deleting a system parameter.
func systemDeleteAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	key := cmd.Args().First()
	if key == "" {
		return cli.Exit("Error: parameter key is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteSystemParameterWithResponse(ctx, key)
	if err != nil {
		return cliCtx.HandleError("Failed to delete system parameter", err)
	}

	if resp.StatusCode() != 200 && resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to delete system parameter", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Success("Parameter deleted successfully: " + key)
	}

	output.PrintSuccess(os.Stdout, "Parameter deleted successfully: "+key)
	return nil
}

// systemJobsStatsAction shows aggregate job & check-schedule stats across all orgs.
func systemJobsStatsAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetSystemJobStatsWithResponse(ctx)
	if err != nil {
		return cliCtx.HandleError("Failed to get system job stats", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get system job stats", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderJobStats(resp.JSON200.Data)

	return nil
}

// systemJobsListAction lists background jobs across all orgs (super-admin).
func systemJobsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	base := buildJobListParams(cmd)
	params := &client.ListSystemJobsParams{
		Status: base.Status,
		Type:   base.Type,
		Limit:  base.Limit,
		Offset: base.Offset,
	}

	resp, err := apiClient.ListSystemJobsWithResponse(ctx, params)
	if err != nil {
		return cliCtx.HandleError("Failed to list system jobs", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list system jobs", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderAdminJobList(resp.JSON200.Data)

	return nil
}

// systemJobsGetAction shows a single background job across all orgs (super-admin).
func systemJobsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	jobUID, err := requireJobUID(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetSystemJobWithResponse(ctx, jobUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get system job", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get system job", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderAdminJobDetail(resp.JSON200.Data)

	return nil
}

// systemJobsChainAction shows a job's retry chain across all orgs (super-admin).
func systemJobsChainAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	jobUID, err := requireJobUID(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetSystemJobChainWithResponse(ctx, jobUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get system job chain", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get system job chain", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderAdminJobList(resp.JSON200.Data)

	return nil
}

// systemCheckJobsListAction lists check-schedule jobs across all orgs (super-admin).
func systemCheckJobsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.ListSystemCheckJobsParams{}
	if cmd.IsSet(flagLimit) {
		l := cmd.Int(flagLimit)
		params.Limit = &l
	}
	if cmd.IsSet(flagOffset) {
		o := cmd.Int(flagOffset)
		params.Offset = &o
	}

	resp, err := apiClient.ListSystemCheckJobsWithResponse(ctx, params)
	if err != nil {
		return cliCtx.HandleError("Failed to list system check jobs", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list system check jobs", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderCheckJobList(resp.JSON200.Data)

	return nil
}

// systemCheckJobsGetAction shows a single check-schedule job across all orgs (super-admin).
func systemCheckJobsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	jobUID, err := requireJobUID(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetSystemCheckJobWithResponse(ctx, jobUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get system check job", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get system check job", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderCheckJobDetail(resp.JSON200.Data)

	return nil
}
