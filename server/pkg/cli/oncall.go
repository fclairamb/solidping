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

// On-call CLI shared strings, kept as constants to satisfy goconst and keep
// flag names / column headers consistent across the on-call commands.
const (
	colTimezone = "TIMEZONE"
	colRotation = "ROTATION"
	colOnCall   = "ON-CALL"
	colFrom     = "FROM"
	colTo       = "TO"
	colReason   = "REASON"
	colUser     = "USER"

	flagTimezone       = "timezone"
	flagRotationType   = "rotation-type"
	flagHandoffTime    = "handoff-time"
	flagHandoffWeekday = "handoff-weekday"
	flagUser           = "user"
	flagReason         = "reason"
	flagFrom           = "from"
	flagUntil          = "until"
	flagDays           = "days"

	msgScheduleUIDRequired = "Error: on-call schedule uid is required"
	msgOverrideArgsReq     = "Error: <schedule-uid> <override-uid> are required"
)

// oncallScheduleUIDArg reads and validates the schedule UID from the first arg.
func oncallScheduleUIDArg(cmd *cli.Command) (openapi_types.UUID, error) {
	raw := cmd.Args().First()
	if raw == "" {
		return uuid.Nil, cli.Exit(msgScheduleUIDRequired, 5)
	}

	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, cli.Exit("Error: invalid on-call schedule uid: "+err.Error(), 5)
	}

	return parsed, nil
}

// renderOncallSchedule prints a single schedule in text mode.
func renderOncallSchedule(schedule *client.OncallSchedule) {
	output.PrintMessage(os.Stdout, "UID:          "+schedule.Uid.String())
	output.PrintMessage(os.Stdout, "Name:         "+schedule.Name)
	output.PrintMessage(os.Stdout, "Timezone:     "+schedule.Timezone)
	output.PrintMessage(os.Stdout, "Rotation:     "+schedule.RotationType)
	output.PrintMessage(os.Stdout, "Handoff time: "+schedule.HandoffTime)

	if schedule.HandoffWeekday != nil {
		output.PrintMessage(os.Stdout, "Handoff day:  "+strconv.Itoa(*schedule.HandoffWeekday))
	}

	output.PrintMessage(os.Stdout, "Start:        "+schedule.StartAt.Format(time.RFC3339))
	output.PrintMessage(os.Stdout, "iCal enabled: "+strconv.FormatBool(schedule.IcalEnabled))

	if schedule.Description != nil && *schedule.Description != "" {
		output.PrintMessage(os.Stdout, "Description:  "+*schedule.Description)
	}

	if schedule.CurrentlyOnCall != nil {
		output.PrintMessage(os.Stdout, "On-call now:  "+schedule.CurrentlyOnCall.Name+
			" <"+schedule.CurrentlyOnCall.Email+">")
	}
}

// oncallListAction lists all on-call schedules for the org.
func oncallListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListOncallSchedulesWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list on-call schedules", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list on-call schedules", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No on-call schedules found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colName, colTimezone, colRotation, colOnCall})

	for i := range *resp.JSON200.Data {
		schedule := &(*resp.JSON200.Data)[i]

		onCall := ""
		if schedule.CurrentlyOnCall != nil {
			onCall = schedule.CurrentlyOnCall.Name
		}

		tbl.AppendRow(table.Row{
			schedule.Uid.String(),
			schedule.Name,
			schedule.Timezone,
			schedule.RotationType,
			onCall,
		})
	}

	tbl.Render()

	return nil
}

// oncallCreateAction creates a new on-call schedule.
func oncallCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	name := cmd.String(flagName)
	if name == "" || cmd.String(flagTimezone) == "" || cmd.String(flagRotationType) == "" ||
		cmd.String(flagHandoffTime) == "" {
		return cli.Exit("Error: --name, --timezone, --rotation-type and --handoff-time are required", 5)
	}

	startAt := time.Now()
	if cmd.IsSet(flagStart) {
		parsed, perr := parseTimeFlag(cmd, flagStart)
		if perr != nil {
			return perr
		}

		startAt = *parsed
	}

	userUIDs, err := parseUIDSlice(cmd.StringSlice(flagUser))
	if err != nil {
		return err
	}

	body := client.CreateOncallScheduleJSONRequestBody{
		Name:           name,
		Description:    optString(cmd, flagDescription),
		Timezone:       cmd.String(flagTimezone),
		RotationType:   cmd.String(flagRotationType),
		HandoffTime:    cmd.String(flagHandoffTime),
		HandoffWeekday: optInt(cmd, flagHandoffWeekday),
		StartAt:        startAt,
	}

	if len(userUIDs) > 0 {
		body.UserUids = &userUIDs
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateOncallScheduleWithResponse(ctx, cliCtx.GetOrg(), body)
	if err != nil {
		return cliCtx.HandleError("Failed to create on-call schedule", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create on-call schedule", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Created on-call schedule "+resp.JSON201.Uid.String())
	renderOncallSchedule(resp.JSON201)

	return nil
}

// oncallGetAction fetches a single on-call schedule by UID.
func oncallGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	scheduleUID, err := oncallScheduleUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetOncallScheduleWithResponse(ctx, cliCtx.GetOrg(), scheduleUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get on-call schedule", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get on-call schedule", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderOncallSchedule(resp.JSON200)

	return nil
}

// oncallUpdateAction patches an on-call schedule.
func oncallUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	scheduleUID, err := oncallScheduleUIDArg(cmd)
	if err != nil {
		return err
	}

	startAt, err := parseTimeFlag(cmd, flagStart)
	if err != nil {
		return err
	}

	body := client.UpdateOncallScheduleJSONRequestBody{
		Name:           optString(cmd, flagName),
		Description:    optString(cmd, flagDescription),
		Timezone:       optString(cmd, flagTimezone),
		RotationType:   optString(cmd, flagRotationType),
		HandoffTime:    optString(cmd, flagHandoffTime),
		HandoffWeekday: optInt(cmd, flagHandoffWeekday),
		StartAt:        startAt,
	}

	if cmd.IsSet(flagUser) {
		userUIDs, uerr := parseUIDSlice(cmd.StringSlice(flagUser))
		if uerr != nil {
			return uerr
		}

		body.UserUids = &userUIDs
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateOncallScheduleWithResponse(ctx, cliCtx.GetOrg(), scheduleUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to update on-call schedule", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update on-call schedule", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Updated on-call schedule "+resp.JSON200.Uid.String())
	renderOncallSchedule(resp.JSON200)

	return nil
}

// oncallRemoveAction deletes an on-call schedule by UID.
func oncallRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	scheduleUID, err := oncallScheduleUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteOncallScheduleWithResponse(ctx, cliCtx.GetOrg(), scheduleUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove on-call schedule", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove on-call schedule", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Removed on-call schedule "+scheduleUID.String())

	return nil
}

// oncallPreviewAction previews the rotation over a window.
func oncallPreviewAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	scheduleUID, err := oncallScheduleUIDArg(cmd)
	if err != nil {
		return err
	}

	from, err := parseTimeFlag(cmd, flagFrom)
	if err != nil {
		return err
	}

	params := &client.PreviewOncallScheduleParams{
		From: from,
		Days: optInt(cmd, flagDays),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.PreviewOncallScheduleWithResponse(ctx, cliCtx.GetOrg(), scheduleUID, params)
	if err != nil {
		return cliCtx.HandleError("Failed to preview on-call schedule", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to preview on-call schedule", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No preview slots in the requested window")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUser, colFrom, colTo})

	for i := range *resp.JSON200.Data {
		slot := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			slot.UserUid.String(),
			slot.From.Format(time.RFC3339),
			slot.To.Format(time.RFC3339),
		})
	}

	tbl.Render()

	return nil
}

// oncallOverridesListAction lists overrides on a schedule.
func oncallOverridesListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	scheduleUID, err := oncallScheduleUIDArg(cmd)
	if err != nil {
		return err
	}

	from, err := parseTimeFlag(cmd, flagFrom)
	if err != nil {
		return err
	}

	until, err := parseTimeFlag(cmd, flagUntil)
	if err != nil {
		return err
	}

	params := &client.ListOncallOverridesParams{From: from, Until: until}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListOncallOverridesWithResponse(ctx, cliCtx.GetOrg(), scheduleUID, params)
	if err != nil {
		return cliCtx.HandleError("Failed to list overrides", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list overrides", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No overrides found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colUser, colStart, colEnd, colReason})

	for i := range *resp.JSON200.Data {
		override := &(*resp.JSON200.Data)[i]

		reason := ""
		if override.Reason != nil {
			reason = *override.Reason
		}

		tbl.AppendRow(table.Row{
			override.UID.String(),
			override.UserUID.String(),
			override.StartAt.Format(time.RFC3339),
			override.EndAt.Format(time.RFC3339),
			reason,
		})
	}

	tbl.Render()

	return nil
}

// oncallOverridesCreateAction creates an override on a schedule.
func oncallOverridesCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	scheduleUID, err := oncallScheduleUIDArg(cmd)
	if err != nil {
		return err
	}

	if cmd.String(flagUser) == "" || !cmd.IsSet(flagStart) || !cmd.IsSet(flagEnd) {
		return cli.Exit("Error: --user, --start and --end are required", 5)
	}

	userUID, err := uuid.Parse(cmd.String(flagUser))
	if err != nil {
		return cli.Exit("Error: invalid user uid: "+err.Error(), 5)
	}

	startAt, err := parseTimeFlag(cmd, flagStart)
	if err != nil {
		return err
	}

	endAt, err := parseTimeFlag(cmd, flagEnd)
	if err != nil {
		return err
	}

	body := client.CreateOncallOverrideJSONRequestBody{
		UserUid: userUID,
		StartAt: *startAt,
		EndAt:   *endAt,
		Reason:  optString(cmd, flagReason),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateOncallOverrideWithResponse(ctx, cliCtx.GetOrg(), scheduleUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to create override", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create override", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Created override "+resp.JSON201.UID.String())

	return nil
}

// oncallOverridesRemoveAction deletes an override.
func oncallOverridesRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 2 {
		return cli.Exit(msgOverrideArgsReq, 5)
	}

	scheduleUID, err := uuid.Parse(cmd.Args().Get(0))
	if err != nil {
		return cli.Exit("Error: invalid on-call schedule uid: "+err.Error(), 5)
	}

	overrideUID, err := uuid.Parse(cmd.Args().Get(1))
	if err != nil {
		return cli.Exit("Error: invalid override uid: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteOncallOverrideWithResponse(ctx, cliCtx.GetOrg(), scheduleUID, overrideUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove override", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove override", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Removed override "+overrideUID.String())

	return nil
}

// printICalFeed prints the secret + URL returned by enable/rotate.
func printICalFeed(cliCtx *Context, secret, feedURL, action string) error {
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(map[string]string{"secret": secret, flagURL: feedURL})
	}

	output.PrintSuccess(os.Stdout, "iCal feed "+action)
	output.PrintMessage(os.Stdout, "Secret: "+secret)
	output.PrintMessage(os.Stdout, "URL:    "+feedURL)

	return nil
}

// oncallICalEnableAction enables the iCal feed for a schedule.
func oncallICalEnableAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	scheduleUID, err := oncallScheduleUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.EnableOncallIcalFeedWithResponse(ctx, cliCtx.GetOrg(), scheduleUID)
	if err != nil {
		return cliCtx.HandleError("Failed to enable iCal feed", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to enable iCal feed", resp.StatusCode())
	}

	return printICalFeed(cliCtx, resp.JSON200.Secret, resp.JSON200.Url, "enabled")
}

// oncallICalRotateAction rotates the iCal feed secret for a schedule.
func oncallICalRotateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	scheduleUID, err := oncallScheduleUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.RotateOncallIcalFeedWithResponse(ctx, cliCtx.GetOrg(), scheduleUID)
	if err != nil {
		return cliCtx.HandleError("Failed to rotate iCal feed", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to rotate iCal feed", resp.StatusCode())
	}

	return printICalFeed(cliCtx, resp.JSON200.Secret, resp.JSON200.Url, "rotated")
}

// oncallICalDisableAction disables the iCal feed for a schedule.
func oncallICalDisableAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	scheduleUID, err := oncallScheduleUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DisableOncallIcalFeedWithResponse(ctx, cliCtx.GetOrg(), scheduleUID)
	if err != nil {
		return cliCtx.HandleError("Failed to disable iCal feed", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to disable iCal feed", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "iCal feed disabled")

	return nil
}

// oncallICalFeedAction fetches the raw public iCal feed by its secret.
func oncallICalFeedAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	secret := cmd.Args().First()
	if secret == "" {
		return cli.Exit("Error: feed secret is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	feed, err := apiClient.GetOnCallICalFeed(ctx, secret)
	if err != nil {
		return cliCtx.HandleError("Failed to fetch iCal feed", err)
	}

	output.PrintMessage(os.Stdout, feed)

	return nil
}
