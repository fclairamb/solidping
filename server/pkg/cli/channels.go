package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Channel CLI shared strings, kept as constants to satisfy goconst and keep
// flag names / column headers consistent across the channel commands.
const (
	colEnabled = "ENABLED"
	colDefault = "DEFAULT"

	flagSettings  = "settings"
	flagDefault   = "default"
	flagNoDefault = "no-default"
	flagEnabled   = "enabled"
	flagDisabled  = "disabled"

	msgChannelUIDRequired = "Error: channel uid is required"
	msgCheckRequired      = "Error: check uid or slug is required"
)

// parseChannelSettings decodes the --settings flag (a JSON object string) into a
// map suitable for the generated request body. An empty value yields nil.
func parseChannelSettings(raw string) (*map[string]interface{}, error) {
	if raw == "" {
		return nil, nil //nolint:nilnil // absent settings are represented by a nil map
	}

	settings := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return nil, fmt.Errorf("invalid --settings JSON: %w", err)
	}

	return &settings, nil
}

// channelUIDArg reads and validates the channel UID from the first argument.
func channelUIDArg(cmd *cli.Command) (openapi_types.UUID, error) {
	raw := cmd.Args().First()
	if raw == "" {
		return uuid.Nil, cli.Exit(msgChannelUIDRequired, 5)
	}

	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, cli.Exit("Error: invalid channel uid: "+err.Error(), 5)
	}

	return parsed, nil
}

// renderChannel prints a single channel in text mode.
func renderChannel(channel *client.Channel) {
	output.PrintMessage(os.Stdout, "UID:      "+channel.Uid.String())
	output.PrintMessage(os.Stdout, "Type:     "+channel.Type)
	output.PrintMessage(os.Stdout, "Name:     "+channel.Name)
	output.PrintMessage(os.Stdout, "Enabled:  "+strconv.FormatBool(channel.Enabled))
	output.PrintMessage(os.Stdout, "Default:  "+strconv.FormatBool(channel.IsDefault))

	if channel.SettingsPrivateKeys != nil && len(*channel.SettingsPrivateKeys) > 0 {
		output.PrintMessage(os.Stdout, "Secret settings:")
		for _, key := range *channel.SettingsPrivateKeys {
			output.PrintMessage(os.Stdout, "  - "+key)
		}
	}
}

// channelsListAction lists all notification channels for the org.
func channelsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.ListChannelsParams{}
	if channelType := cmd.String(flagType); channelType != "" {
		params.Type = &channelType
	}

	resp, err := apiClient.ListChannelsWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list channels", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list channels", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No channels found")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colType, colName, colEnabled, colDefault})

	for i := range *resp.JSON200.Data {
		channel := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			channel.Uid.String(),
			channel.Type,
			channel.Name,
			strconv.FormatBool(channel.Enabled),
			strconv.FormatBool(channel.IsDefault),
		})
	}

	tbl.Render()

	return nil
}

// channelsGetAction fetches a single channel by UID.
func channelsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	channelUID, err := channelUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetChannelWithResponse(ctx, cliCtx.GetOrg(), channelUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get channel", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get channel", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderChannel(resp.JSON200)

	return nil
}

// channelsCreateAction creates a new notification channel.
func channelsCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	channelType := cmd.String(flagType)
	name := cmd.String(flagName)
	if channelType == "" || name == "" {
		return cli.Exit("Error: --type and --name are required", 5)
	}

	settings, err := parseChannelSettings(cmd.String(flagSettings))
	if err != nil {
		return cli.Exit("Error: "+err.Error(), 5)
	}

	body := client.CreateChannelJSONRequestBody{
		Type:     channelType,
		Name:     name,
		Settings: settings,
	}

	if cmd.Bool(flagDisabled) {
		disabled := false
		body.Enabled = &disabled
	}

	if cmd.Bool(flagDefault) {
		isDefault := true
		body.IsDefault = &isDefault
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateChannelWithResponse(ctx, cliCtx.GetOrg(), body)
	if err != nil {
		return cliCtx.HandleError("Failed to create channel", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create channel", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Created channel "+resp.JSON201.Uid.String())
	renderChannel(resp.JSON201)

	return nil
}

// channelsUpdateAction patches a channel's name/enabled/default/settings.
func channelsUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	channelUID, err := channelUIDArg(cmd)
	if err != nil {
		return err
	}

	settings, err := parseChannelSettings(cmd.String(flagSettings))
	if err != nil {
		return cli.Exit("Error: "+err.Error(), 5)
	}

	body := client.UpdateChannelJSONRequestBody{Settings: settings}

	if cmd.IsSet(flagName) {
		name := cmd.String(flagName)
		body.Name = &name
	}

	if cmd.Bool(flagEnabled) {
		enabled := true
		body.Enabled = &enabled
	} else if cmd.Bool(flagDisabled) {
		disabled := false
		body.Enabled = &disabled
	}

	if cmd.Bool(flagDefault) {
		isDefault := true
		body.IsDefault = &isDefault
	} else if cmd.Bool(flagNoDefault) {
		notDefault := false
		body.IsDefault = &notDefault
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateChannelWithResponse(ctx, cliCtx.GetOrg(), channelUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to update channel", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update channel", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Updated channel "+resp.JSON200.Uid.String())
	renderChannel(resp.JSON200)

	return nil
}

// channelsRemoveAction deletes a channel by UID.
func channelsRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	channelUID, err := channelUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteChannelWithResponse(ctx, cliCtx.GetOrg(), channelUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove channel", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove channel", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Removed channel "+channelUID.String())

	return nil
}

// channelsRotateSecretAction rotates a webhook channel's signing secret.
func channelsRotateSecretAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	channelUID, err := channelUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.RotateChannelSecretWithResponse(ctx, cliCtx.GetOrg(), channelUID)
	if err != nil {
		return cliCtx.HandleError("Failed to rotate channel secret", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to rotate channel secret", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Rotated signing secret for channel "+channelUID.String())

	return nil
}

// channelsTestAction sends a sample notification through a channel.
func channelsTestAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	channelUID, err := channelUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.TestChannelWithResponse(ctx, cliCtx.GetOrg(), channelUID)
	if err != nil {
		return cliCtx.HandleError("Failed to test channel", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to test channel", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	result := resp.JSON200
	if result.Success {
		output.PrintSuccess(os.Stdout, "Test delivery succeeded")
	} else {
		msg := "Test delivery failed"
		if result.Error != nil && *result.Error != "" {
			msg += ": " + *result.Error
		}
		output.PrintError(os.Stdout, msg)
	}

	output.PrintMessage(os.Stdout, "Status code: "+strconv.Itoa(result.StatusCode))
	output.PrintMessage(os.Stdout, "Duration:    "+strconv.FormatInt(result.DurationMs, 10)+" ms")

	if result.Detail != nil && *result.Detail != "" {
		output.PrintMessage(os.Stdout, "Detail:      "+*result.Detail)
	}

	// A failed delivery is a non-zero exit so scripts can gate on it.
	if !result.Success {
		return cli.Exit("", 1)
	}

	return nil
}

// checksChannelsListAction lists the channels bound to a check.
func checksChannelsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit(msgCheckRequired, 5)
	}

	check := cmd.Args().Get(0)

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListCheckChannelsWithResponse(ctx, cliCtx.GetOrg(), check)
	if err != nil {
		return cliCtx.HandleError("Failed to list check channels", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list check channels", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No channels bound to this check")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colType, colName, colEnabled, colDefault})

	for i := range *resp.JSON200.Data {
		channel := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			channel.Uid.String(),
			channel.Type,
			channel.Name,
			strconv.FormatBool(channel.Enabled),
			strconv.FormatBool(channel.IsDefault),
		})
	}

	tbl.Render()

	return nil
}

// checksChannelsSetAction replaces the full set of channels bound to a check.
func checksChannelsSetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit(msgCheckRequired, 5)
	}

	check := cmd.Args().Get(0)

	connectionUIDs := make([]openapi_types.UUID, 0, cmd.Args().Len()-1)
	for i := 1; i < cmd.Args().Len(); i++ {
		parsed, parseErr := uuid.Parse(cmd.Args().Get(i))
		if parseErr != nil {
			return cli.Exit("Error: invalid channel uid "+cmd.Args().Get(i)+": "+parseErr.Error(), 5)
		}
		connectionUIDs = append(connectionUIDs, parsed)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	body := client.SetCheckChannelsJSONRequestBody{ConnectionUids: connectionUIDs}

	resp, err := apiClient.SetCheckChannelsWithResponse(ctx, cliCtx.GetOrg(), check, body)
	if err != nil {
		return cliCtx.HandleError("Failed to set check channels", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to set check channels", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, fmt.Sprintf("Set %d channel(s) on %s", len(connectionUIDs), check))

	return nil
}

// checkChannelBindingArgs validates the shared <check> <connection-uid> args.
func checkChannelBindingArgs(cmd *cli.Command) (string, openapi_types.UUID, error) {
	if cmd.Args().Len() < 2 {
		return "", uuid.Nil, cli.Exit("Error: <check> <connection-uid> are required", 5)
	}

	check := cmd.Args().Get(0)

	connectionUID, err := uuid.Parse(cmd.Args().Get(1))
	if err != nil {
		return "", uuid.Nil, cli.Exit("Error: invalid channel uid: "+err.Error(), 5)
	}

	return check, connectionUID, nil
}

// checksChannelsAddAction binds a single channel to a check.
func checksChannelsAddAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	check, connectionUID, err := checkChannelBindingArgs(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.AddCheckChannelWithResponse(ctx, cliCtx.GetOrg(), check, connectionUID)
	if err != nil {
		return cliCtx.HandleError("Failed to add check channel", err)
	}

	if resp.StatusCode() != 201 {
		return cliCtx.HandleStatusError("Failed to add check channel", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, fmt.Sprintf("Bound channel %s to %s", connectionUID.String(), check))

	return nil
}

// checksChannelsRemoveAction unbinds a single channel from a check.
func checksChannelsRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	check, connectionUID, err := checkChannelBindingArgs(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.RemoveCheckChannelWithResponse(ctx, cliCtx.GetOrg(), check, connectionUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove check channel", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove check channel", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, fmt.Sprintf("Unbound channel %s from %s", connectionUID.String(), check))

	return nil
}
