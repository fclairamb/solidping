package cli

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Maintenance-window CLI shared strings, kept as constants to satisfy goconst
// and keep flag names / column headers consistent across the commands.
const (
	colStart      = "START"
	colEnd        = "END"
	colRecurrence = "RECURRENCE"

	flagStart         = "start"
	flagEnd           = "end"
	flagRecurrence    = "recurrence"
	flagRecurrenceEnd = "recurrence-end"
	flagGroup         = "group"

	msgWindowUIDRequired = "Error: maintenance window uid is required"
	usageRFC3339         = "RFC3339 timestamp (e.g. 2026-07-11T14:00:00Z)"
)

// parseTimeFlag parses an RFC3339 flag value when set, else returns nil.
func parseTimeFlag(cmd *cli.Command, name string) (*time.Time, error) {
	if !cmd.IsSet(name) {
		return nil, nil //nolint:nilnil // absent optional time is represented by a nil pointer
	}

	parsed, err := time.Parse(time.RFC3339, cmd.String(name))
	if err != nil {
		return nil, cli.Exit("Error: invalid "+name+" time ("+usageRFC3339+"): "+err.Error(), 5)
	}

	return &parsed, nil
}

// maintenanceWindowUIDArg reads and validates the window UID from the first arg.
func maintenanceWindowUIDArg(cmd *cli.Command) (openapi_types.UUID, error) {
	raw := cmd.Args().First()
	if raw == "" {
		return uuid.Nil, cli.Exit(msgWindowUIDRequired, 5)
	}

	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, cli.Exit("Error: invalid maintenance window uid: "+err.Error(), 5)
	}

	return parsed, nil
}

// renderMaintenanceWindow prints a single maintenance window in text mode.
func renderMaintenanceWindow(window *client.MaintenanceWindow) {
	output.PrintMessage(os.Stdout, "UID:         "+window.Uid.String())
	output.PrintMessage(os.Stdout, "Title:       "+window.Title)
	output.PrintMessage(os.Stdout, "Status:      "+window.Status)
	output.PrintMessage(os.Stdout, "Start:       "+window.StartAt.Format(time.RFC3339))
	output.PrintMessage(os.Stdout, "End:         "+window.EndAt.Format(time.RFC3339))
	output.PrintMessage(os.Stdout, "Recurrence:  "+window.Recurrence)

	if window.RecurrenceEnd != nil {
		output.PrintMessage(os.Stdout, "Recur. end:  "+window.RecurrenceEnd.Format(time.RFC3339))
	}

	if window.Description != nil && *window.Description != "" {
		output.PrintMessage(os.Stdout, "Description: "+*window.Description)
	}
}

// maintenanceWindowsListAction lists all maintenance windows for the org.
func maintenanceWindowsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.ListMaintenanceWindowsParams{
		Status: optString(cmd, flagStatus),
		Limit:  optInt(cmd, flagLimit),
	}

	resp, err := apiClient.ListMaintenanceWindowsWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list maintenance windows", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list maintenance windows", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No maintenance windows found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colTitle, colStatus, colStart, colEnd, colRecurrence})

	for i := range *resp.JSON200.Data {
		window := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			window.Uid.String(),
			window.Title,
			window.Status,
			window.StartAt.Format(time.RFC3339),
			window.EndAt.Format(time.RFC3339),
			window.Recurrence,
		})
	}

	tbl.Render()

	return nil
}

// maintenanceWindowsCreateAction creates a new maintenance window.
func maintenanceWindowsCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	title := cmd.String(flagTitle)
	if title == "" || !cmd.IsSet(flagStart) || !cmd.IsSet(flagEnd) {
		return cli.Exit("Error: --title, --start and --end are required", 5)
	}

	startAt, err := parseTimeFlag(cmd, flagStart)
	if err != nil {
		return err
	}

	endAt, err := parseTimeFlag(cmd, flagEnd)
	if err != nil {
		return err
	}

	recurrenceEnd, err := parseTimeFlag(cmd, flagRecurrenceEnd)
	if err != nil {
		return err
	}

	body := client.CreateMaintenanceWindowJSONRequestBody{
		Title:         title,
		Description:   optString(cmd, flagDescription),
		StartAt:       *startAt,
		EndAt:         *endAt,
		Recurrence:    optString(cmd, flagRecurrence),
		RecurrenceEnd: recurrenceEnd,
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateMaintenanceWindowWithResponse(ctx, cliCtx.GetOrg(), body)
	if err != nil {
		return cliCtx.HandleError("Failed to create maintenance window", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create maintenance window", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Created maintenance window "+resp.JSON201.Uid.String())
	renderMaintenanceWindow(resp.JSON201)

	return nil
}

// maintenanceWindowsGetAction fetches a single maintenance window by UID.
func maintenanceWindowsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	windowUID, err := maintenanceWindowUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetMaintenanceWindowWithResponse(ctx, cliCtx.GetOrg(), windowUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get maintenance window", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get maintenance window", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderMaintenanceWindow(resp.JSON200)

	return nil
}

// maintenanceWindowsUpdateAction patches a maintenance window.
func maintenanceWindowsUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	windowUID, err := maintenanceWindowUIDArg(cmd)
	if err != nil {
		return err
	}

	startAt, err := parseTimeFlag(cmd, flagStart)
	if err != nil {
		return err
	}

	endAt, err := parseTimeFlag(cmd, flagEnd)
	if err != nil {
		return err
	}

	recurrenceEnd, err := parseTimeFlag(cmd, flagRecurrenceEnd)
	if err != nil {
		return err
	}

	body := client.UpdateMaintenanceWindowJSONRequestBody{
		Title:         optString(cmd, flagTitle),
		Description:   optString(cmd, flagDescription),
		StartAt:       startAt,
		EndAt:         endAt,
		Recurrence:    optString(cmd, flagRecurrence),
		RecurrenceEnd: recurrenceEnd,
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateMaintenanceWindowWithResponse(ctx, cliCtx.GetOrg(), windowUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to update maintenance window", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update maintenance window", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Updated maintenance window "+resp.JSON200.Uid.String())
	renderMaintenanceWindow(resp.JSON200)

	return nil
}

// maintenanceWindowsRemoveAction deletes a maintenance window by UID.
func maintenanceWindowsRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	windowUID, err := maintenanceWindowUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteMaintenanceWindowWithResponse(ctx, cliCtx.GetOrg(), windowUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove maintenance window", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove maintenance window", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Removed maintenance window "+windowUID.String())

	return nil
}

// maintenanceWindowsChecksListAction lists the checks covered by a window.
func maintenanceWindowsChecksListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	windowUID, err := maintenanceWindowUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListMaintenanceWindowChecksWithResponse(ctx, cliCtx.GetOrg(), windowUID)
	if err != nil {
		return cliCtx.HandleError("Failed to list maintenance window checks", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list maintenance window checks", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No checks covered by this maintenance window")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colCheck, "CHECK GROUP"})

	for i := range *resp.JSON200.Data {
		assoc := &(*resp.JSON200.Data)[i]

		checkUID := ""
		if assoc.CheckUid != nil {
			checkUID = assoc.CheckUid.String()
		}

		groupUID := ""
		if assoc.CheckGroupUid != nil {
			groupUID = assoc.CheckGroupUid.String()
		}

		tbl.AppendRow(table.Row{assoc.Uid.String(), checkUID, groupUID})
	}

	tbl.Render()

	return nil
}

// maintenanceWindowsChecksSetAction replaces the checks/groups covered by a
// window. Check UIDs are positional args after the window uid; group UIDs are
// passed via repeatable --group flags.
func maintenanceWindowsChecksSetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	windowUID, err := maintenanceWindowUIDArg(cmd)
	if err != nil {
		return err
	}

	checkUIDs, err := parseUIDList(cmd, 1)
	if err != nil {
		return err
	}

	groupUIDs, err := parseUIDSlice(cmd.StringSlice(flagGroup))
	if err != nil {
		return err
	}

	body := client.SetMaintenanceWindowChecksJSONRequestBody{}
	if len(checkUIDs) > 0 {
		body.CheckUids = &checkUIDs
	}

	if len(groupUIDs) > 0 {
		body.CheckGroupUids = &groupUIDs
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.SetMaintenanceWindowChecksWithResponse(ctx, cliCtx.GetOrg(), windowUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to set maintenance window checks", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to set maintenance window checks", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Set "+strconv.Itoa(len(checkUIDs))+" check(s) and "+
		strconv.Itoa(len(groupUIDs))+" group(s) on "+windowUID.String())

	return nil
}

// parseUIDSlice parses a slice of string UIDs into UUIDs.
func parseUIDSlice(raw []string) ([]openapi_types.UUID, error) {
	uids := make([]openapi_types.UUID, 0, len(raw))
	for _, value := range raw {
		parsed, err := uuid.Parse(value)
		if err != nil {
			return nil, cli.Exit("Error: invalid uid "+value+": "+err.Error(), 5)
		}

		uids = append(uids, parsed)
	}

	return uids, nil
}
