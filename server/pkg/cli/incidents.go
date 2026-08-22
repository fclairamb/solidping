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

// stateUnknown is the default state for incidents.
const stateUnknown = "unknown"

// incidentsListAction handles listing incidents.
//
//nolint:funlen,cyclop // CLI parameter parsing and output formatting
func incidentsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Get API client
	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	// Build query parameters
	params := &client.ListIncidentsParams{}

	if checkUID := cmd.String(flagCheck); checkUID != "" {
		params.CheckUid = &checkUID
	}

	if state := cmd.String("state"); state != "" {
		params.State = &state
	}

	if cursor := cmd.String("cursor"); cursor != "" {
		params.Cursor = &cursor
	}

	if size := cmd.Int(flagSize); size > 0 {
		params.Size = &size
	}

	// Always include check details to display slug
	withCheckArg := flagCheck
	params.With = &withCheckArg

	// Call API
	resp, err := apiClient.ListIncidentsWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list incidents", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list incidents", resp.StatusCode())
	}

	// Non-text output
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	// Table output
	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No incidents found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, "STATE", colCheck, "STARTED", "DURATION", "TITLE"})

	for i := range *resp.JSON200.Data {
		incident := &(*resp.JSON200.Data)[i]

		state := stateUnknown
		if incident.State != nil {
			state = string(*incident.State)
		}

		checkInfo := ""
		if incident.CheckSlug != nil {
			checkInfo = *incident.CheckSlug
		} else if incident.Check != nil && incident.Check.Slug != nil {
			checkInfo = *incident.Check.Slug
		}

		startedAt := ""
		if incident.StartedAt != nil {
			startedAt = incident.StartedAt.Format(time.RFC3339)
		}

		duration := calcIncidentDuration(incident.StartedAt, incident.ResolvedAt)

		title := ""
		if incident.Title != nil {
			title = *incident.Title
		}

		uid := ""
		if incident.Uid != nil {
			uid = incident.Uid.String()
		}

		tbl.AppendRow(table.Row{uid, state, checkInfo, startedAt, duration, title})
	}

	tbl.Render()

	// Print pagination info
	if resp.JSON200.Pagination != nil && resp.JSON200.Pagination.Cursor != nil && *resp.JSON200.Pagination.Cursor != "" {
		output.PrintMessage(os.Stdout, "\nNext cursor: "+*resp.JSON200.Pagination.Cursor)
	}

	return nil
}

// incidentsGetAction handles getting a single incident.
func incidentsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Get incident UID from args
	incidentUIDStr := cmd.Args().First()
	if incidentUIDStr == "" {
		return cli.Exit("Error: incident UID is required", 5)
	}

	// Parse UUID
	incidentUID, err := uuid.Parse(incidentUIDStr)
	if err != nil {
		return cli.Exit("Error: invalid incident UID: "+err.Error(), 5)
	}

	// Get API client
	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	// Call API. nil preserves today's behavior: the handler always populates
	// checkSlug/checkName regardless of "with", and this command doesn't print
	// the opt-in check/members fields, so no params are needed.
	resp, err := apiClient.GetIncidentWithResponse(ctx, cliCtx.GetOrg(), incidentUID, nil)
	if err != nil {
		return cliCtx.HandleError("Failed to get incident", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get incident", resp.StatusCode())
	}

	// Non-text output
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	// Text output
	incident := resp.JSON200

	output.PrintMessage(os.Stdout, "UID:           "+safeUUID(incident.Uid))
	output.PrintMessage(os.Stdout, "State:         "+safeState(incident.State))
	output.PrintMessage(os.Stdout, "Check UID:     "+safeUUID(incident.CheckUid))
	if incident.CheckSlug != nil {
		output.PrintMessage(os.Stdout, "Check Slug:    "+*incident.CheckSlug)
	}
	if incident.Title != nil {
		output.PrintMessage(os.Stdout, "Title:         "+*incident.Title)
	}
	if incident.StartedAt != nil {
		output.PrintMessage(os.Stdout, "Started At:    "+incident.StartedAt.Format(time.RFC3339))
	}
	if incident.ResolvedAt != nil {
		output.PrintMessage(os.Stdout, "Resolved At:   "+incident.ResolvedAt.Format(time.RFC3339))
	}
	if incident.EscalatedAt != nil {
		output.PrintMessage(os.Stdout, "Escalated At:  "+incident.EscalatedAt.Format(time.RFC3339))
	}
	if incident.FailureCount != nil {
		output.PrintMessage(os.Stdout, "Failures:      "+strconv.Itoa(*incident.FailureCount))
	}

	return nil
}

// incidentsEventsAction handles listing events for a specific incident.
//
//nolint:cyclop,funlen // CLI parameter parsing and output formatting
func incidentsEventsAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Get incident UID from args
	incidentUIDStr := cmd.Args().First()
	if incidentUIDStr == "" {
		return cli.Exit("Error: incident UID is required", 5)
	}

	incidentUID, err := uuid.Parse(incidentUIDStr)
	if err != nil {
		return cli.Exit("Error: invalid incident UID: "+err.Error(), 5)
	}

	// Get API client
	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	// Build params
	params := &client.ListIncidentEventsParams{}
	if cursor := cmd.String("cursor"); cursor != "" {
		params.Cursor = &cursor
	}
	if size := cmd.Int(flagSize); size > 0 {
		params.Size = &size
	}

	// Call API
	resp, err := apiClient.ListIncidentEventsWithResponse(ctx, cliCtx.GetOrg(), incidentUID, params)
	if err != nil {
		return cliCtx.HandleError("Failed to list incident events", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list incident events", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No events found")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colTimestamp, colType, colActor})

	for i := range *resp.JSON200.Data {
		event := &(*resp.JSON200.Data)[i]
		timestamp := ""
		if event.CreatedAt != nil {
			timestamp = event.CreatedAt.Format(time.RFC3339)
		}
		eventType := ""
		if event.EventType != nil {
			eventType = *event.EventType
		}
		actor := ""
		if event.ActorType != nil {
			actor = string(*event.ActorType)
		}
		tbl.AppendRow(table.Row{timestamp, eventType, actor})
	}

	tbl.Render()

	if resp.JSON200.Pagination != nil && resp.JSON200.Pagination.Cursor != nil && *resp.JSON200.Pagination.Cursor != "" {
		output.PrintMessage(os.Stdout, "\nNext cursor: "+*resp.JSON200.Pagination.Cursor)
	}

	return nil
}

// parseIncidentUID reads and validates the incident UID from the first arg.
func parseIncidentUID(cmd *cli.Command) (uuid.UUID, error) {
	incidentUIDStr := cmd.Args().First()
	if incidentUIDStr == "" {
		return uuid.Nil, cli.Exit("Error: incident UID is required", 5)
	}

	incidentUID, err := uuid.Parse(incidentUIDStr)
	if err != nil {
		return uuid.Nil, cli.Exit("Error: invalid incident UID: "+err.Error(), 5)
	}

	return incidentUID, nil
}

// renderIncidentActionResult prints the updated incident after a lifecycle action.
func renderIncidentActionResult(cliCtx *Context, incident *client.IncidentDetail, successMsg string) error {
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(incident)
	}

	output.PrintSuccess(os.Stdout, successMsg)

	if incident.State != nil {
		output.PrintMessage(os.Stdout, "State: "+string(*incident.State))
	}

	return nil
}

// incidentsAckAction acknowledges an incident (POST /incidents/:uid/ack).
func incidentsAckAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	incidentUID, err := parseIncidentUID(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	body := client.AcknowledgeIncidentJSONRequestBody{}
	if note := cmd.String("note"); note != "" {
		body.Note = &note
	}

	resp, err := apiClient.AcknowledgeIncidentWithResponse(ctx, cliCtx.GetOrg(), incidentUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to acknowledge incident", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to acknowledge incident", resp.StatusCode())
	}

	return renderIncidentActionResult(cliCtx, resp.JSON200, "Incident acknowledged")
}

// incidentsUnackAction removes an acknowledgement (POST /incidents/:uid/unack).
func incidentsUnackAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	incidentUID, err := parseIncidentUID(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UnacknowledgeIncidentWithResponse(ctx, cliCtx.GetOrg(), incidentUID)
	if err != nil {
		return cliCtx.HandleError("Failed to unacknowledge incident", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to unacknowledge incident", resp.StatusCode())
	}

	return renderIncidentActionResult(cliCtx, resp.JSON200, "Incident acknowledgement cleared")
}

// incidentsSnoozeAction snoozes an incident (POST /incidents/:uid/snooze).
func incidentsSnoozeAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	incidentUID, err := parseIncidentUID(cmd)
	if err != nil {
		return err
	}

	body := client.SnoozeIncidentJSONRequestBody{}
	if duration := cmd.String("duration"); duration != "" {
		body.Duration = &duration
	}
	if until := cmd.String("until"); until != "" {
		parsed, parseErr := time.Parse(time.RFC3339, until)
		if parseErr != nil {
			return cli.Exit("Error: --until must be an RFC3339 timestamp: "+parseErr.Error(), 5)
		}
		body.Until = &parsed
	}
	if reason := cmd.String("reason"); reason != "" {
		body.Reason = &reason
	}

	if body.Duration == nil && body.Until == nil {
		return cli.Exit("Error: one of --duration or --until is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.SnoozeIncidentWithResponse(ctx, cliCtx.GetOrg(), incidentUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to snooze incident", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to snooze incident", resp.StatusCode())
	}

	return renderIncidentActionResult(cliCtx, resp.JSON200, "Incident snoozed")
}

// incidentsUnsnoozeAction clears a snooze (POST /incidents/:uid/unsnooze).
func incidentsUnsnoozeAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	incidentUID, err := parseIncidentUID(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UnsnoozeIncidentWithResponse(ctx, cliCtx.GetOrg(), incidentUID)
	if err != nil {
		return cliCtx.HandleError("Failed to unsnooze incident", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to unsnooze incident", resp.StatusCode())
	}

	return renderIncidentActionResult(cliCtx, resp.JSON200, "Incident snooze cleared")
}

// incidentsResolveAction resolves an incident (POST /incidents/:uid/resolve).
func incidentsResolveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	incidentUID, err := parseIncidentUID(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	body := client.ResolveIncidentJSONRequestBody{}
	if note := cmd.String("note"); note != "" {
		body.Note = &note
	}

	resp, err := apiClient.ResolveIncidentWithResponse(ctx, cliCtx.GetOrg(), incidentUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to resolve incident", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to resolve incident", resp.StatusCode())
	}

	return renderIncidentActionResult(cliCtx, resp.JSON200, "Incident resolved")
}

func safeUUID(uid *openapi_types.UUID) string {
	if uid == nil {
		return ""
	}

	return uid.String()
}

func safeState(state *client.IncidentDetailState) string {
	if state == nil {
		return stateUnknown
	}

	return string(*state)
}

// calcIncidentDuration calculates and formats the duration of an incident.
// For ongoing incidents, it shows time since start.
// For resolved incidents, it shows the total duration.
func calcIncidentDuration(startedAt, resolvedAt *time.Time) string {
	if startedAt == nil {
		return ""
	}

	var duration time.Duration
	if resolvedAt != nil {
		// Resolved incident: show total duration
		duration = resolvedAt.Sub(*startedAt)
	} else {
		// Ongoing incident: show time since start
		duration = time.Since(*startedAt)
	}

	return formatDuration(duration)
}
