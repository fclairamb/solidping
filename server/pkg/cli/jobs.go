package cli

import (
	"context"
	"encoding/json"
	"os"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// jobsListAction handles listing jobs.
func jobsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.ListJobsParams{}
	if t := cmd.String("type"); t != "" {
		params.Type = &t
	}
	if s := cmd.String(flagStatus); s != "" {
		params.Status = &s
	}

	resp, err := apiClient.ListJobsWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list jobs", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list jobs", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No jobs found")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colType, colStatus, colCreated})

	for i := range *resp.JSON200.Data {
		job := &(*resp.JSON200.Data)[i]
		status := ""
		if job.Status != nil {
			status = string(*job.Status)
		}
		tbl.AppendRow(table.Row{
			safeUUID(job.Uid),
			safeStr(job.Type),
			status,
			safeTime(job.CreatedAt),
		})
	}

	tbl.Render()
	return nil
}

// jobsGetAction handles getting a job's details.
func jobsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	uidStr := cmd.Args().First()
	if uidStr == "" {
		return cli.Exit("Error: job UID is required", 5)
	}

	jobUID, err := uuid.Parse(uidStr)
	if err != nil {
		return cli.Exit("Error: invalid job UID: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetJobWithResponse(ctx, cliCtx.GetOrg(), jobUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get job", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get job", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	job := resp.JSON200.Data
	if job == nil {
		output.PrintMessage(os.Stdout, "No job data")
		return nil
	}

	output.PrintMessage(os.Stdout, "UID:       "+safeUUID(job.Uid))
	output.PrintMessage(os.Stdout, "Type:      "+safeStr(job.Type))
	if job.Status != nil {
		output.PrintMessage(os.Stdout, "Status:    "+string(*job.Status))
	}
	output.PrintMessage(os.Stdout, "Created:   "+safeTime(job.CreatedAt))
	output.PrintMessage(os.Stdout, "Started:   "+safeTime(job.StartedAt))
	output.PrintMessage(os.Stdout, "Completed: "+safeTime(job.CompletedAt))

	return nil
}

// jobsCreateAction handles creating a job.
func jobsCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	jobType := cmd.String("type")
	if jobType == "" {
		return cli.Exit("Error: --type is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	req := client.CreateJobJSONRequestBody{
		Type: jobType,
	}

	// Parse config if provided
	if configStr := cmd.String("config"); configStr != "" {
		var configMap map[string]interface{}
		if parseErr := json.Unmarshal([]byte(configStr), &configMap); parseErr != nil {
			return cli.Exit("Error: --config must be valid JSON: "+parseErr.Error(), 5)
		}
		req.Config = &configMap
	}

	resp, err := apiClient.CreateJobWithResponse(ctx, cliCtx.GetOrg(), req)
	if err != nil {
		return cliCtx.HandleError("Failed to create job", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create job", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Job created successfully!")
	if resp.JSON201.Data != nil {
		output.PrintMessage(os.Stdout, "  UID:  "+safeUUID(resp.JSON201.Data.Uid))
		output.PrintMessage(os.Stdout, "  Type: "+safeStr(resp.JSON201.Data.Type))
	}

	return nil
}

// jobsCancelAction handles canceling a job.
func jobsCancelAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	uidStr := cmd.Args().First()
	if uidStr == "" {
		return cli.Exit("Error: job UID is required", 5)
	}

	jobUID, err := uuid.Parse(uidStr)
	if err != nil {
		return cli.Exit("Error: invalid job UID: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CancelJobWithResponse(ctx, cliCtx.GetOrg(), jobUID)
	if err != nil {
		return cliCtx.HandleError("Failed to cancel job", err)
	}

	if resp.StatusCode() != 200 && resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to cancel job", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Success("Job canceled successfully: " + uidStr)
	}

	output.PrintSuccess(os.Stdout, "Job canceled successfully: "+uidStr)
	return nil
}
