package cli

import (
	"context"
	"os"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Status-update CLI shared strings, kept as constants to satisfy goconst.
const (
	colKind      = "KIND"
	colTitle     = "TITLE"
	colPublished = "PUBLISHED"

	flagStatusPage = "status-page"
	flagSection    = "section"
	flagIncident   = "incident"
	flagKind       = "kind"
	flagTitle      = "title"
	flagBody       = "body"
	flagLink       = "link"
	flagLimit      = "limit"
	flagOffset     = "offset"

	msgStatusUpdateUIDRequired = "Error: status update uid is required"

	timeFormatSeconds = "2006-01-02 15:04:05"
)

// statusUpdateUIDArg reads and validates the status-update UID from the first argument.
func statusUpdateUIDArg(cmd *cli.Command) (openapi_types.UUID, error) {
	raw := cmd.Args().First()
	if raw == "" {
		return uuid.Nil, cli.Exit(msgStatusUpdateUIDRequired, 5)
	}

	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, cli.Exit("Error: invalid status update uid: "+err.Error(), 5)
	}

	return parsed, nil
}

// renderStatusUpdate prints a single status update in text mode.
func renderStatusUpdate(update *client.StatusUpdate) {
	output.PrintMessage(os.Stdout, "UID:          "+update.Uid.String())
	output.PrintMessage(os.Stdout, "Status page:  "+update.StatusPageUid.String())
	output.PrintMessage(os.Stdout, "Kind:         "+update.Kind)
	output.PrintMessage(os.Stdout, "Title:        "+update.Title)
	output.PrintMessage(os.Stdout, "Published:    "+update.PublishedAt.Format(timeFormatSeconds))
	output.PrintMessage(os.Stdout, "Body:         "+update.BodyMarkdown)
}

// statusUpdatesListAction lists status updates for the org with filtering.
func statusUpdatesListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.ListStatusUpdatesParams{
		StatusPage: optString(cmd, flagStatusPage),
		Section:    optString(cmd, flagSection),
		Check:      optString(cmd, flagCheck),
		Incident:   optString(cmd, flagIncident),
		Limit:      optInt(cmd, flagLimit),
		Offset:     optInt(cmd, flagOffset),
	}

	resp, err := apiClient.ListStatusUpdatesWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list status updates", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list status updates", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No status updates found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colKind, colTitle, colPublished})

	for i := range *resp.JSON200.Data {
		update := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			update.Uid.String(),
			update.Kind,
			update.Title,
			update.PublishedAt.Format(timeFormatSeconds),
		})
	}

	tbl.Render()

	return nil
}

// statusUpdatesCreateAction creates a new status update.
func statusUpdatesCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	statusPage := cmd.String(flagStatusPage)
	title := cmd.String(flagTitle)
	body := cmd.String(flagBody)
	kind := cmd.String(flagKind)
	if statusPage == "" || title == "" || body == "" || kind == "" {
		return cli.Exit("Error: --status-page, --title, --body and --kind are required", 5)
	}

	statusPageUID, err := uuid.Parse(statusPage)
	if err != nil {
		return cli.Exit("Error: invalid --status-page uid: "+err.Error(), 5)
	}

	sectionUID, err := optUUID(cmd, flagSection)
	if err != nil {
		return err
	}

	checkUID, err := optUUID(cmd, flagCheck)
	if err != nil {
		return err
	}

	incidentUID, err := optUUID(cmd, flagIncident)
	if err != nil {
		return err
	}

	reqBody := client.CreateStatusUpdateJSONRequestBody{
		StatusPageUid: statusPageUID,
		Title:         title,
		BodyMarkdown:  body,
		Kind:          kind,
		SectionUid:    sectionUID,
		CheckUid:      checkUID,
		IncidentUid:   incidentUID,
		LinkUrl:       optString(cmd, flagLink),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateStatusUpdateWithResponse(ctx, cliCtx.GetOrg(), reqBody)
	if err != nil {
		return cliCtx.HandleError("Failed to create status update", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create status update", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Created status update "+resp.JSON201.Uid.String())
	renderStatusUpdate(resp.JSON201)

	return nil
}

// statusUpdatesGetAction fetches a single status update by UID.
func statusUpdatesGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	updateUID, err := statusUpdateUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetStatusUpdateWithResponse(ctx, cliCtx.GetOrg(), updateUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get status update", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get status update", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderStatusUpdate(resp.JSON200)

	return nil
}

// statusUpdatesUpdateAction patches a status update.
func statusUpdatesUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	updateUID, err := statusUpdateUIDArg(cmd)
	if err != nil {
		return err
	}

	sectionUID, err := optUUID(cmd, flagSection)
	if err != nil {
		return err
	}

	checkUID, err := optUUID(cmd, flagCheck)
	if err != nil {
		return err
	}

	incidentUID, err := optUUID(cmd, flagIncident)
	if err != nil {
		return err
	}

	reqBody := client.UpdateStatusUpdateJSONRequestBody{
		Title:        optString(cmd, flagTitle),
		BodyMarkdown: optString(cmd, flagBody),
		Kind:         optString(cmd, flagKind),
		LinkUrl:      optString(cmd, flagLink),
		SectionUid:   sectionUID,
		CheckUid:     checkUID,
		IncidentUid:  incidentUID,
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateStatusUpdateWithResponse(ctx, cliCtx.GetOrg(), updateUID, reqBody)
	if err != nil {
		return cliCtx.HandleError("Failed to update status update", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update status update", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Updated status update "+resp.JSON200.Uid.String())
	renderStatusUpdate(resp.JSON200)

	return nil
}

// statusUpdatesRemoveAction deletes a status update by UID.
func statusUpdatesRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	updateUID, err := statusUpdateUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteStatusUpdateWithResponse(ctx, cliCtx.GetOrg(), updateUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove status update", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove status update", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Removed status update "+updateUID.String())

	return nil
}
