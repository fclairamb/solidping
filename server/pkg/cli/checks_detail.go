package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// checksGetAction handles getting a single check by UID or slug.
//
//nolint:cyclop,funlen // CLI output formatting
func checksGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	identifier := cmd.Args().First()
	if identifier == "" {
		return cli.Exit("Error: check UID or slug is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetCheckWithResponse(ctx, cliCtx.GetOrg(), identifier)
	if err != nil {
		return cliCtx.HandleError("Failed to get check", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get check", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	check := resp.JSON200
	output.PrintMessage(os.Stdout, "UID:         "+safeUUID(check.Uid))
	if check.Slug != nil {
		output.PrintMessage(os.Stdout, "Slug:        "+*check.Slug)
	}
	if check.Name != nil {
		output.PrintMessage(os.Stdout, "Name:        "+*check.Name)
	}
	if check.Type != nil {
		output.PrintMessage(os.Stdout, "Type:        "+string(*check.Type))
	}
	if check.Period != nil {
		output.PrintMessage(os.Stdout, "Period:      "+*check.Period)
	}
	if check.Enabled != nil {
		enabled := boolYes
		if !*check.Enabled {
			enabled = boolNo
		}
		output.PrintMessage(os.Stdout, "Enabled:     "+enabled)
	}
	if check.Description != nil && *check.Description != "" {
		output.PrintMessage(os.Stdout, "Description: "+*check.Description)
	}
	if check.Config != nil {
		if url, ok := (*check.Config)["url"]; ok {
			output.PrintMessage(os.Stdout, fmt.Sprintf("URL:         %v", url))
		}
	}
	if check.LastStatusChange != nil && check.LastStatusChange.Status != nil {
		status := string(*check.LastStatusChange.Status)
		if check.LastStatusChange.Time != nil {
			status += " (since " + check.LastStatusChange.Time.Format(time.RFC3339) + ")"
		}
		output.PrintMessage(os.Stdout, "Status:      "+status)
	}
	if check.CreatedAt != nil {
		output.PrintMessage(os.Stdout, "Created:     "+check.CreatedAt.Format(time.RFC3339))
	}

	return nil
}

// checksUpdateAction handles updating a check.
//
//nolint:cyclop,funlen // CLI flag handling
func checksUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	identifier := cmd.Args().First()
	if identifier == "" {
		return cli.Exit("Error: check UID or slug is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	// Build update request with only provided fields
	req := client.UpdateCheckJSONRequestBody{}
	hasChanges := false

	if cmd.IsSet("name") {
		name := cmd.String("name")
		req.Name = &name
		hasChanges = true
	}

	if cmd.IsSet("slug") {
		slug := cmd.String("slug")
		req.Slug = &slug
		hasChanges = true
	}

	if cmd.IsSet("enabled") {
		enabled := cmd.Bool("enabled")
		req.Enabled = &enabled
		hasChanges = true
	}

	if cmd.IsSet("disabled") {
		enabled := false
		req.Enabled = &enabled
		hasChanges = true
	}

	if cmd.IsSet("interval") {
		period := cmd.String("interval")
		if d, parseErr := time.ParseDuration(period); parseErr == nil {
			hours := int(d.Hours())
			minutes := int(d.Minutes()) % 60
			seconds := int(d.Seconds()) % 60
			period = fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
		}
		req.Period = &period
		hasChanges = true
	}

	if !hasChanges {
		return cli.Exit("Error: at least one field must be specified to update", 5)
	}

	resp, err := apiClient.UpdateCheckWithResponse(ctx, cliCtx.GetOrg(), identifier, req)
	if err != nil {
		return cliCtx.HandleError("Failed to update check", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update check", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Check updated successfully: "+identifier)
	check := resp.JSON200
	if check.Slug != nil {
		output.PrintMessage(os.Stdout, "  Slug: "+*check.Slug)
	}
	if check.Name != nil {
		output.PrintMessage(os.Stdout, "  Name: "+*check.Name)
	}
	if check.Enabled != nil {
		enabled := boolYes
		if !*check.Enabled {
			enabled = boolNo
		}
		output.PrintMessage(os.Stdout, "  Enabled: "+enabled)
	}

	return nil
}

// checksUpsertAction handles upserting a check by slug.
//
//nolint:cyclop,funlen // CLI flag handling
func checksUpsertAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 2 {
		return cli.Exit("Error: slug and URL are required", 5)
	}

	slug := cmd.Args().Get(0)
	url := cmd.Args().Get(1)

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	// Build config
	checkConfig := map[string]interface{}{
		"url": url,
	}
	if timeout := cmd.String("timeout"); timeout != "" {
		checkConfig["timeout"] = timeout
	}

	req := client.UpsertCheckJSONRequestBody{
		Config: checkConfig,
	}

	if cmd.IsSet("type") {
		t := client.UpsertCheckRequestType(cmd.String("type"))
		req.Type = &t
	}
	if cmd.IsSet("name") {
		name := cmd.String("name")
		req.Name = &name
	}
	if cmd.IsSet("interval") {
		period := cmd.String("interval")
		if d, parseErr := time.ParseDuration(period); parseErr == nil {
			hours := int(d.Hours())
			minutes := int(d.Minutes()) % 60
			seconds := int(d.Seconds()) % 60
			period = fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
		}
		req.Period = &period
	}

	resp, err := apiClient.UpsertCheckWithResponse(ctx, cliCtx.GetOrg(), slug, req)
	if err != nil {
		return cliCtx.HandleError("Failed to upsert check", err)
	}

	// Handle both 200 (updated) and 201 (created)
	var check *client.Check
	action := "Updated"

	switch resp.StatusCode() {
	case 200:
		check = resp.JSON200
	case 201:
		check = resp.JSON201
		action = "Created"
	default:
		return cliCtx.HandleStatusError("Failed to upsert check", resp.StatusCode())
	}

	if check == nil {
		return cliCtx.HandleStatusError("Failed to upsert check", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(map[string]interface{}{
			"action": action,
			"check":  check,
		})
	}

	output.PrintSuccess(os.Stdout, action+" check: "+slug)
	if check.Uid != nil {
		output.PrintMessage(os.Stdout, "  UID: "+check.Uid.String())
	}
	if check.Name != nil {
		output.PrintMessage(os.Stdout, "  Name: "+*check.Name)
	}

	return nil
}

// checksEventsAction handles listing events for a specific check.
//
//nolint:cyclop,funlen // CLI parameter parsing and output formatting
func checksEventsAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	identifier := cmd.Args().First()
	if identifier == "" {
		return cli.Exit("Error: check UID or slug is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.ListCheckEventsParams{}
	if cursor := cmd.String("cursor"); cursor != "" {
		params.Cursor = &cursor
	}
	if size := cmd.Int("size"); size > 0 {
		params.Size = &size
	}

	resp, err := apiClient.ListCheckEventsWithResponse(ctx, cliCtx.GetOrg(), identifier, params)
	if err != nil {
		return cliCtx.HandleError("Failed to list check events", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list check events", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No events found")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{"TIMESTAMP", "TYPE", "ACTOR"})

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
		tbl.AppendRow(table.Row{timestamp, eventType, actor})
	}

	tbl.Render()

	if resp.JSON200.Pagination != nil && resp.JSON200.Pagination.Cursor != nil && *resp.JSON200.Pagination.Cursor != "" {
		output.PrintMessage(os.Stdout, "\nNext cursor: "+*resp.JSON200.Pagination.Cursor)
	}

	return nil
}
