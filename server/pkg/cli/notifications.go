package cli

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Notification CLI shared strings.
const (
	flagConnection = "connection"
	flagBefore     = "before"
	flagValue      = "value"
	flagLabel      = "label"
	flagOrder      = "order"

	colLabel   = "LABEL"
	colChannel = "CHANNEL"
	colEvent   = "EVENT"
	colTarget  = "TARGET"

	msgNotifUIDRequired   = "Error: notification uid is required"
	msgRouteUIDRequired   = "Error: notification route uid is required"
	msgContactUIDRequired = "Error: notification contact uid is required"
)

// notifTimeBefore parses the --before flag into an RFC3339 timestamp pointer.
// Returns (nil, nil) when the flag is unset.
func notifTimeBefore(cmd *cli.Command) (*time.Time, error) {
	if !cmd.IsSet(flagBefore) {
		return nil, nil //nolint:nilnil // absent optional filter is a nil pointer
	}

	parsed, err := time.Parse(time.RFC3339, cmd.String(flagBefore))
	if err != nil {
		return nil, cli.Exit("Error: --before must be an RFC3339 timestamp: "+err.Error(), 5)
	}

	return &parsed, nil
}

// renderNotificationList prints a NotificationListResponse as a table.
func renderNotificationList(cliCtx *Context, resp *client.NotificationListResponse) error {
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp)
	}

	if resp.Data == nil || len(*resp.Data) == 0 {
		output.PrintMessage(os.Stdout, "No notifications found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colStatus, colChannel, colEvent, colTarget, colCreated})

	for i := range *resp.Data {
		notif := &(*resp.Data)[i]
		tbl.AppendRow(table.Row{
			safeStr(notif.Uid),
			safeStr(notif.Status),
			safeStr(notif.ChannelType),
			safeStr(notif.EventType),
			notificationTarget(notif),
			safeTime(notif.CreatedAt),
		})
	}

	tbl.Render()

	return nil
}

// notificationTarget picks the most descriptive target name for a row.
func notificationTarget(notif *client.Notification) string {
	if notif.User != nil {
		return safeStr(notif.User.Name)
	}

	if notif.Connection != nil {
		return safeStr(notif.Connection.Name)
	}

	return ""
}

// notificationsListAction lists notification deliveries. The scope is selected
// by flags: --incident (per-incident), --connection (org by connection),
// --user (a specific user, admin), or the caller (default / --me).
func notificationsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	before, err := notifTimeBefore(cmd)
	if err != nil {
		return err
	}

	status := optString(cmd, flagStatus)
	connection := optString(cmd, flagConnection)
	limit := optInt(cmd, flagLimit)

	switch {
	case cmd.IsSet(flagIncident):
		return notificationsListIncident(ctx, cliCtx, apiClient, cmd, status, connection, limit, before)
	case cmd.IsSet(flagConnection) && !cmd.IsSet(flagUser):
		return notificationsListByConnection(ctx, cliCtx, apiClient, cmd.String(flagConnection), limit)
	case cmd.IsSet(flagUser):
		return notificationsListUser(ctx, cliCtx, apiClient, cmd.String(flagUser), status, connection, limit, before)
	default:
		return notificationsListMe(ctx, cliCtx, apiClient, status, connection, limit, before)
	}
}

func notificationsListIncident(
	ctx context.Context, cliCtx *Context, apiClient *client.SolidPingClient, cmd *cli.Command,
	status, connection *string, limit *int, before *time.Time,
) error {
	incidentUID, err := uuid.Parse(cmd.String(flagIncident))
	if err != nil {
		return cli.Exit("Error: invalid incident UID: "+err.Error(), 5)
	}

	params := &client.ListIncidentNotificationsParams{
		Status:        status,
		ConnectionUid: connection,
		Limit:         limit,
		Before:        before,
	}

	resp, err := apiClient.ListIncidentNotificationsWithResponse(ctx, cliCtx.GetOrg(), incidentUID, params)
	if err != nil {
		return cliCtx.HandleError("Failed to list incident notifications", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list incident notifications", resp.StatusCode())
	}

	return renderNotificationList(cliCtx, resp.JSON200)
}

func notificationsListByConnection(
	ctx context.Context, cliCtx *Context, apiClient *client.SolidPingClient, connectionUID string, limit *int,
) error {
	params := &client.ListNotificationsParams{
		ConnectionUid: connectionUID,
		Limit:         limit,
	}

	resp, err := apiClient.ListNotificationsWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list notifications", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list notifications", resp.StatusCode())
	}

	return renderNotificationList(cliCtx, resp.JSON200)
}

func notificationsListUser(
	ctx context.Context, cliCtx *Context, apiClient *client.SolidPingClient, userUID string,
	status, connection *string, limit *int, before *time.Time,
) error {
	params := &client.ListUserNotificationsParams{
		Status:        status,
		ConnectionUid: connection,
		Limit:         limit,
		Before:        before,
	}

	resp, err := apiClient.ListUserNotificationsWithResponse(ctx, cliCtx.GetOrg(), userUID, params)
	if err != nil {
		return cliCtx.HandleError("Failed to list user notifications", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list user notifications", resp.StatusCode())
	}

	return renderNotificationList(cliCtx, resp.JSON200)
}

func notificationsListMe(
	ctx context.Context, cliCtx *Context, apiClient *client.SolidPingClient,
	status, connection *string, limit *int, before *time.Time,
) error {
	params := &client.ListMyNotificationsParams{
		Status:        status,
		ConnectionUid: connection,
		Limit:         limit,
		Before:        before,
	}

	resp, err := apiClient.ListMyNotificationsWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list notifications", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list notifications", resp.StatusCode())
	}

	return renderNotificationList(cliCtx, resp.JSON200)
}

// notificationsGetAction fetches a single notification delivery by UID. With
// --incident it scopes to that incident; otherwise it uses the org-scoped route.
func notificationsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	notifUID := cmd.Args().First()
	if notifUID == "" {
		return cli.Exit(msgNotifUIDRequired, 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	if cmd.IsSet(flagIncident) {
		return notificationsGetIncident(ctx, cliCtx, apiClient, cmd.String(flagIncident), notifUID)
	}

	resp, err := apiClient.GetNotificationWithResponse(ctx, cliCtx.GetOrg(), notifUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get notification", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get notification", resp.StatusCode())
	}

	return renderNotificationDetail(cliCtx, resp.JSON200)
}

func notificationsGetIncident(
	ctx context.Context, cliCtx *Context, apiClient *client.SolidPingClient, incident, notifUID string,
) error {
	incidentUID, err := uuid.Parse(incident)
	if err != nil {
		return cli.Exit("Error: invalid incident UID: "+err.Error(), 5)
	}

	resp, err := apiClient.GetIncidentNotificationWithResponse(ctx, cliCtx.GetOrg(), incidentUID, notifUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get incident notification", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get incident notification", resp.StatusCode())
	}

	return renderNotificationDetail(cliCtx, resp.JSON200)
}

// renderNotificationDetail prints a NotificationDetail in text mode.
func renderNotificationDetail(cliCtx *Context, detail *client.NotificationDetail) error {
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(detail)
	}

	output.PrintMessage(os.Stdout, "UID:          "+safeStr(detail.Uid))
	output.PrintMessage(os.Stdout, "Incident UID: "+safeStr(detail.IncidentUid))
	output.PrintMessage(os.Stdout, "Event:        "+safeStr(detail.EventType))
	output.PrintMessage(os.Stdout, "Source:       "+safeStr(detail.Source))
	output.PrintMessage(os.Stdout, "Channel:      "+safeStr(detail.ChannelType))
	output.PrintMessage(os.Stdout, "Status:       "+safeStr(detail.Status))

	if detail.SkipReason != nil {
		output.PrintMessage(os.Stdout, "Skip reason:  "+*detail.SkipReason)
	}

	if detail.Error != nil {
		output.PrintMessage(os.Stdout, "Error:        "+*detail.Error)
	}

	if detail.MessageId != nil {
		output.PrintMessage(os.Stdout, "Message ID:   "+*detail.MessageId)
	}

	if detail.User != nil {
		output.PrintMessage(os.Stdout, "User:         "+safeStr(detail.User.Name)+" ("+safeStr(detail.User.Uid)+")")
	}

	if detail.Connection != nil {
		output.PrintMessage(os.Stdout, "Connection:   "+safeStr(detail.Connection.Name)+
			" ["+safeStr(detail.Connection.Type)+"]")
	}

	output.PrintMessage(os.Stdout, "Created:      "+safeTime(detail.CreatedAt))
	output.PrintMessage(os.Stdout, "Sent:         "+safeTime(detail.SentAt))

	return nil
}
