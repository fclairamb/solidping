package cli

import (
	"context"
	"encoding/json"
	"os"
	"strconv"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Job-admin table columns and shared labels.
const (
	colRegion    = "REGION"
	colState     = "STATE"
	colScheduled = "SCHEDULED"
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

// jobAdminListFlags is the shared flag set for the admin/system job-list commands.
func jobAdminListFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: flagStatus, Usage: "Filter by job status"},
		&cli.StringFlag{Name: flagType, Usage: "Filter by job type"},
		&cli.IntFlag{Name: flagLimit, Usage: "Maximum results (1-200)"},
		&cli.IntFlag{Name: flagOffset, Usage: "Result offset"},
	}
}

// checkJobPaginationFlags is the shared flag set for the check-jobs list commands.
func checkJobPaginationFlags() []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{Name: flagLimit, Usage: "Maximum results (1-200)"},
		&cli.IntFlag{Name: flagOffset, Usage: "Result offset"},
	}
}

// buildJobListParams reads the shared status/type/limit/offset flags used by the
// admin & system job-list commands.
func buildJobListParams(cmd *cli.Command) *client.ListAdminJobsParams {
	params := &client.ListAdminJobsParams{}
	if s := cmd.String(flagStatus); s != "" {
		params.Status = &s
	}
	if t := cmd.String(flagType); t != "" {
		params.Type = &t
	}
	if cmd.IsSet(flagLimit) {
		l := cmd.Int(flagLimit)
		params.Limit = &l
	}
	if cmd.IsSet(flagOffset) {
		o := cmd.Int(flagOffset)
		params.Offset = &o
	}

	return params
}

// jobsStatsAction shows aggregate job & check-schedule stats for the org.
func jobsStatsAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetJobStatsWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to get job stats", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get job stats", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderJobStats(resp.JSON200.Data)

	return nil
}

// jobsAdminListAction lists background jobs for the org (admin-gated).
func jobsAdminListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListAdminJobsWithResponse(ctx, cliCtx.GetOrg(), buildJobListParams(cmd))
	if err != nil {
		return cliCtx.HandleError("Failed to list admin jobs", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list admin jobs", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderAdminJobList(resp.JSON200.Data)

	return nil
}

// jobsAdminGetAction shows a single background job for the org (admin-gated).
func jobsAdminGetAction(ctx context.Context, cmd *cli.Command) error {
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

	resp, err := apiClient.GetAdminJobWithResponse(ctx, cliCtx.GetOrg(), jobUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get admin job", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get admin job", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderAdminJobDetail(resp.JSON200.Data)

	return nil
}

// jobsAdminChainAction shows a job's retry chain for the org (admin-gated).
func jobsAdminChainAction(ctx context.Context, cmd *cli.Command) error {
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

	resp, err := apiClient.GetAdminJobChainWithResponse(ctx, cliCtx.GetOrg(), jobUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get job chain", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get job chain", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderAdminJobList(resp.JSON200.Data)

	return nil
}

// checkJobsListAction lists check-schedule jobs for the org (admin-gated).
func checkJobsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.ListCheckJobsParams{}
	if cmd.IsSet(flagLimit) {
		l := cmd.Int(flagLimit)
		params.Limit = &l
	}
	if cmd.IsSet(flagOffset) {
		o := cmd.Int(flagOffset)
		params.Offset = &o
	}

	resp, err := apiClient.ListCheckJobsWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list check jobs", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list check jobs", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderCheckJobList(resp.JSON200.Data)

	return nil
}

// checkJobsGetAction shows a single check-schedule job for the org (admin-gated).
func checkJobsGetAction(ctx context.Context, cmd *cli.Command) error {
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

	resp, err := apiClient.GetCheckJobWithResponse(ctx, cliCtx.GetOrg(), jobUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get check job", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get check job", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderCheckJobDetail(resp.JSON200.Data)

	return nil
}

// requireJobUID parses the first positional arg as a job UID.
func requireJobUID(cmd *cli.Command) (uuid.UUID, error) {
	uidStr := cmd.Args().First()
	if uidStr == "" {
		return uuid.Nil, cli.Exit("Error: job UID is required", 5)
	}

	jobUID, err := uuid.Parse(uidStr)
	if err != nil {
		return uuid.Nil, cli.Exit("Error: invalid job UID: "+err.Error(), 5)
	}

	return jobUID, nil
}

// renderJobStats prints the aggregate job & check-schedule stats.
func renderJobStats(stats *client.JobsStats) {
	if stats == nil {
		output.PrintMessage(os.Stdout, "No stats available")
		return
	}

	output.PrintMessage(os.Stdout, "Jobs queue:")
	if stats.Jobs != nil {
		output.PrintMessage(os.Stdout, "  Pending:       "+intPtrStr(stats.Jobs.Pending))
		output.PrintMessage(os.Stdout, "  Running:       "+intPtrStr(stats.Jobs.Running))
		output.PrintMessage(os.Stdout, "  Failed (24h):  "+intPtrStr(stats.Jobs.Failed24h))
	}

	output.PrintMessage(os.Stdout, "Check schedule:")
	if stats.Checks != nil {
		output.PrintMessage(os.Stdout, "  Total:         "+intPtrStr(stats.Checks.Total))
		output.PrintMessage(os.Stdout, "  Due now:       "+intPtrStr(stats.Checks.DueNow))
		output.PrintMessage(os.Stdout, "  In flight:     "+intPtrStr(stats.Checks.InFlight))
		output.PrintMessage(os.Stdout, "  Stalled:       "+intPtrStr(stats.Checks.Stalled))
		output.PrintMessage(os.Stdout, "  Crash-looping: "+intPtrStr(stats.Checks.CrashLooping))
	}
}

// renderAdminJobList prints a table of background jobs.
func renderAdminJobList(jobs *[]client.AdminJob) {
	if jobs == nil || len(*jobs) == 0 {
		output.PrintMessage(os.Stdout, "No jobs found")
		return
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colType, colStatus, colCreated})

	for i := range *jobs {
		job := &(*jobs)[i]
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
}

// renderAdminJobDetail prints a single background job.
func renderAdminJobDetail(job *client.AdminJob) {
	if job == nil {
		output.PrintMessage(os.Stdout, "No job data")
		return
	}

	output.PrintMessage(os.Stdout, "UID:          "+safeUUID(job.Uid))
	output.PrintMessage(os.Stdout, "Organization: "+safeUUID(job.OrganizationUid))
	output.PrintMessage(os.Stdout, "Type:         "+safeStr(job.Type))
	if job.Status != nil {
		output.PrintMessage(os.Stdout, "Status:       "+string(*job.Status))
	}
	if job.RetryCount != nil {
		output.PrintMessage(os.Stdout, "Retry count:  "+strconv.Itoa(*job.RetryCount))
	}
	output.PrintMessage(os.Stdout, "Scheduled:    "+safeTime(job.ScheduledAt))
	output.PrintMessage(os.Stdout, "Created:      "+safeTime(job.CreatedAt))
	output.PrintMessage(os.Stdout, "Updated:      "+safeTime(job.UpdatedAt))
	if job.PreviousJobUid != nil {
		output.PrintMessage(os.Stdout, "Previous job: "+safeUUID(job.PreviousJobUid))
	}
}

// renderCheckJobList prints a table of check-schedule jobs.
func renderCheckJobList(jobs *[]client.CheckJobView) {
	if jobs == nil || len(*jobs) == 0 {
		output.PrintMessage(os.Stdout, "No check jobs found")
		return
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colCheck, colType, colRegion, colState, colScheduled})

	for i := range *jobs {
		job := &(*jobs)[i]
		state := ""
		if job.State != nil {
			state = string(*job.State)
		}
		tbl.AppendRow(table.Row{
			safeUUID(job.Uid),
			safeStr(job.CheckName),
			safeStr(job.Type),
			safeStr(job.Region),
			state,
			safeTime(job.ScheduledAt),
		})
	}

	tbl.Render()
}

// renderCheckJobDetail prints a single check-schedule job.
func renderCheckJobDetail(job *client.CheckJobView) {
	if job == nil {
		output.PrintMessage(os.Stdout, "No check job data")
		return
	}

	output.PrintMessage(os.Stdout, "UID:          "+safeUUID(job.Uid))
	output.PrintMessage(os.Stdout, "Organization: "+safeUUID(job.OrganizationUid))
	output.PrintMessage(os.Stdout, "Check:        "+safeUUID(job.CheckUid))
	output.PrintMessage(os.Stdout, "Check name:   "+safeStr(job.CheckName))
	output.PrintMessage(os.Stdout, "Type:         "+safeStr(job.Type))
	output.PrintMessage(os.Stdout, "Region:       "+safeStr(job.Region))
	if job.State != nil {
		output.PrintMessage(os.Stdout, "State:        "+string(*job.State))
	}
	if job.PeriodSeconds != nil {
		output.PrintMessage(os.Stdout, "Period (s):   "+strconv.FormatInt(*job.PeriodSeconds, 10))
	}
	output.PrintMessage(os.Stdout, "Scheduled:    "+safeTime(job.ScheduledAt))
	output.PrintMessage(os.Stdout, "Lease expiry: "+safeTime(job.LeaseExpiresAt))
	if job.LeaseStarts != nil {
		output.PrintMessage(os.Stdout, "Lease starts: "+strconv.Itoa(*job.LeaseStarts))
	}
	if job.Encrypted != nil {
		output.PrintMessage(os.Stdout, "Encrypted:    "+strconv.FormatBool(*job.Encrypted))
	}
}

// intPtrStr renders a nullable int pointer, showing 0 for nil.
func intPtrStr(v *int) string {
	if v == nil {
		return "0"
	}

	return strconv.Itoa(*v)
}
