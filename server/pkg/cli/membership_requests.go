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

// Membership-request CLI shared strings.
const (
	flagMessage = "message"

	colOrg = "ORG"

	argOrgSlug = "<org-slug>"

	msgRequestUIDRequired = "Error: membership request uid is required"
	msgOrgSlugRequired    = "Error: organization slug is required"

	usageApproveRole  = "Role to grant the new member: admin, user, viewer (default user)"
	usageRejectReason = "Optional reason shown to the requester"
	usageRequestMsg   = "Optional message to the organization admins"
	usageAdminStatus  = "Filter by status: pending, approved, rejected, canceled"
)

// membershipRequestsCommand builds the "membership-requests" command group,
// covering both the requester side and the org-admin side.
func membershipRequestsCommand() *cli.Command {
	return &cli.Command{
		Name:    "membership-requests",
		Aliases: []string{"membership-request", "mr"},
		Usage:   "Request to join an organization and manage those requests",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:      "request",
				Usage:     "Request to join an organization",
				ArgsUsage: argOrgSlug,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagMessage, Usage: usageRequestMsg},
				},
				Action: membershipRequestsRequestAction,
			},
			{
				Name:   flagList,
				Usage:  "List your own membership requests",
				Action: membershipRequestsListAction,
			},
			{
				Name:      "cancel",
				Usage:     "Cancel one of your membership requests",
				ArgsUsage: argUID,
				Action:    membershipRequestsCancelAction,
			},
			membershipRequestsAdminCommand(),
		},
	}
}

// membershipRequestsAdminCommand builds the org-admin subcommand group.
func membershipRequestsAdminCommand() *cli.Command {
	return &cli.Command{
		Name:  "admin",
		Usage: "Org-admin side: list, approve, and reject pending requests",
		Commands: []*cli.Command{
			{
				Name:  flagList,
				Usage: "List the organization's membership requests",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagStatus, Usage: usageAdminStatus},
				},
				Action: membershipRequestsAdminListAction,
			},
			{
				Name:      "approve",
				Usage:     "Approve a membership request",
				ArgsUsage: argUID,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagRole, Usage: usageApproveRole},
				},
				Action: membershipRequestsApproveAction,
			},
			{
				Name:      "reject",
				Usage:     "Reject a membership request",
				ArgsUsage: argUID,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagReason, Usage: usageRejectReason},
				},
				Action: membershipRequestsRejectAction,
			},
		},
	}
}

// membershipRequestsRequestAction opens a request to join the given org.
func membershipRequestsRequestAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	orgSlug := cmd.Args().First()
	if orgSlug == "" {
		return cli.Exit(msgOrgSlugRequired, 5)
	}

	body := client.CreateMembershipRequestJSONRequestBody{
		OrgSlug: orgSlug,
		Message: optString(cmd, flagMessage),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateMembershipRequestWithResponse(ctx, body)
	if err != nil {
		return cliCtx.HandleError("Failed to create membership request", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create membership request", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	req := resp.JSON201
	output.PrintSuccess(os.Stdout, "Membership request created for "+req.Organization.Slug)
	output.PrintMessage(os.Stdout, "  UID:    "+req.Uid.String())
	output.PrintMessage(os.Stdout, "  Status: "+string(req.Status))

	return nil
}

// membershipRequestsListAction lists the caller's own membership requests.
func membershipRequestsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListMyMembershipRequestsWithResponse(ctx)
	if err != nil {
		return cliCtx.HandleError("Failed to list membership requests", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list membership requests", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No membership requests")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colOrg, colStatus, colCreated})

	for i := range *resp.JSON200.Data {
		req := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			req.Uid.String(),
			req.Organization.Slug,
			string(req.Status),
			req.CreatedAt.Format(time.RFC3339),
		})
	}

	tbl.Render()

	return nil
}

// membershipRequestsCancelAction cancels one of the caller's requests.
func membershipRequestsCancelAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	requestUID, err := membershipRequestUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CancelMembershipRequestWithResponse(ctx, requestUID)
	if err != nil {
		return cliCtx.HandleError("Failed to cancel membership request", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to cancel membership request", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Membership request cancelled: "+requestUID.String())

	return nil
}

// membershipRequestsAdminListAction lists an org's membership requests.
func membershipRequestsAdminListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	params := &client.ListOrgMembershipRequestsParams{}
	if cmd.IsSet(flagStatus) {
		status := client.ListOrgMembershipRequestsParamsStatus(cmd.String(flagStatus))
		params.Status = &status
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListOrgMembershipRequestsWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list membership requests", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list membership requests", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No membership requests")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colEmail, colStatus, colCreated})

	for i := range *resp.JSON200.Data {
		req := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			req.Uid.String(),
			string(req.User.Email),
			string(req.Status),
			req.CreatedAt.Format(time.RFC3339),
		})
	}

	tbl.Render()

	return nil
}

// membershipRequestsApproveAction approves a pending request.
func membershipRequestsApproveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	requestUID, err := membershipRequestUIDArg(cmd)
	if err != nil {
		return err
	}

	body := client.ApproveMembershipRequestJSONRequestBody{}
	if cmd.IsSet(flagRole) {
		role := client.ApproveMembershipRequestRequestRole(cmd.String(flagRole))
		body.Role = &role
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ApproveMembershipRequestWithResponse(ctx, cliCtx.GetOrg(), requestUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to approve membership request", err)
	}

	if resp.StatusCode() != 200 {
		return cliCtx.HandleStatusError("Failed to approve membership request", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Membership request approved: "+requestUID.String())

	return nil
}

// membershipRequestsRejectAction rejects a pending request.
func membershipRequestsRejectAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	requestUID, err := membershipRequestUIDArg(cmd)
	if err != nil {
		return err
	}

	body := client.RejectMembershipRequestJSONRequestBody{
		Reason: optString(cmd, flagReason),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.RejectMembershipRequestWithResponse(ctx, cliCtx.GetOrg(), requestUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to reject membership request", err)
	}

	if resp.StatusCode() != 200 {
		return cliCtx.HandleStatusError("Failed to reject membership request", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Membership request rejected: "+requestUID.String())

	return nil
}

// membershipRequestUIDArg reads and validates the request UID from the first arg.
func membershipRequestUIDArg(cmd *cli.Command) (uuid.UUID, error) {
	raw := cmd.Args().First()
	if raw == "" {
		return uuid.Nil, cli.Exit(msgRequestUIDRequired, 5)
	}

	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, cli.Exit("Error: invalid membership request uid: "+err.Error(), 5)
	}

	return parsed, nil
}
