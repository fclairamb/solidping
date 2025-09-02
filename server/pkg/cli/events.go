package cli

import (
	"context"
	"os"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// eventsListAction handles listing events.
//
//nolint:funlen,cyclop // CLI parameter parsing and output formatting
func eventsListAction(ctx context.Context, cmd *cli.Command) error {
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
	params := &client.ListEventsParams{}

	if eventType := cmd.String("type"); eventType != "" {
		params.EventType = &eventType
	}

	if checkUID := cmd.String("check"); checkUID != "" {
		params.CheckUid = &checkUID
	}

	if incidentUID := cmd.String("incident"); incidentUID != "" {
		params.IncidentUid = &incidentUID
	}

	if cursor := cmd.String("cursor"); cursor != "" {
		params.Cursor = &cursor
	}

	if size := cmd.Int("size"); size > 0 {
		params.Size = &size
	}

	// Call API
	resp, err := apiClient.ListEventsWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list events", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list events", resp.StatusCode())
	}

	// Non-text output
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	// Table output
	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No events found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{"TIMESTAMP", "TYPE", "ACTOR", "INCIDENT", "CHECK"})

	for i := range *resp.JSON200.Data {
		event := &(*resp.JSON200.Data)[i]

		timestamp := ""
		if event.CreatedAt != nil {
			timestamp = event.CreatedAt.Format(time.RFC3339)
		}

		eventType := ""
		if event.EventType != nil {
			eventType = string(*event.EventType)
		}

		actor := ""
		if event.ActorType != nil {
			actor = string(*event.ActorType)
		}

		incidentUID := ""
		if event.IncidentUid != nil {
			incidentUID = event.IncidentUid.String()
		}

		checkUID := ""
		if event.CheckUid != nil {
			checkUID = event.CheckUid.String()
		}

		tbl.AppendRow(table.Row{timestamp, eventType, actor, incidentUID, checkUID})
	}

	tbl.Render()

	// Print pagination info
	if resp.JSON200.Pagination != nil && resp.JSON200.Pagination.Cursor != nil && *resp.JSON200.Pagination.Cursor != "" {
		output.PrintMessage(os.Stdout, "\nNext cursor: "+*resp.JSON200.Pagination.Cursor)
	}

	return nil
}
