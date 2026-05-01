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

// membersListAction handles listing organization members.
func membersListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListMembersWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list members", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list members", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No members found")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, "EMAIL", colName, "ROLE"})

	for i := range *resp.JSON200.Data {
		member := &(*resp.JSON200.Data)[i]
		email := ""
		if member.Email != nil {
			email = string(*member.Email)
		}
		role := ""
		if member.Role != nil {
			role = string(*member.Role)
		}
		tbl.AppendRow(table.Row{
			safeUUID(member.Uid),
			email,
			safeStr(member.Name),
			role,
		})
	}

	tbl.Render()
	return nil
}

// membersAddAction handles adding a member to the organization.
func membersAddAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	email := cmd.Args().First()
	if email == "" {
		return cli.Exit("Error: email is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	role := client.AddMemberRequestRole(cmd.String("role"))
	req := client.AddMemberJSONRequestBody{
		Email: openapi_types.Email(email),
		Role:  role,
	}

	resp, err := apiClient.AddMemberWithResponse(ctx, cliCtx.GetOrg(), req)
	if err != nil {
		return cliCtx.HandleError("Failed to add member", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to add member", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Member added successfully!")
	output.PrintMessage(os.Stdout, "  UID:   "+safeUUID(resp.JSON201.Uid))
	output.PrintMessage(os.Stdout, "  Email: "+email)
	if resp.JSON201.Role != nil {
		output.PrintMessage(os.Stdout, "  Role:  "+string(*resp.JSON201.Role))
	}

	return nil
}

// membersGetAction handles getting a member's details.
func membersGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	uidStr := cmd.Args().First()
	if uidStr == "" {
		return cli.Exit("Error: member UID is required", 5)
	}

	memberUID, err := uuid.Parse(uidStr)
	if err != nil {
		return cli.Exit("Error: invalid member UID: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetMemberWithResponse(ctx, cliCtx.GetOrg(), memberUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get member", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get member", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	member := resp.JSON200
	output.PrintMessage(os.Stdout, "UID:     "+safeUUID(member.Uid))
	if member.Email != nil {
		output.PrintMessage(os.Stdout, "Email:   "+string(*member.Email))
	}
	output.PrintMessage(os.Stdout, "Name:    "+safeStr(member.Name))
	if member.Role != nil {
		output.PrintMessage(os.Stdout, "Role:    "+string(*member.Role))
	}
	output.PrintMessage(os.Stdout, "Joined:  "+safeTime(member.JoinedAt))
	output.PrintMessage(os.Stdout, "Created: "+safeTime(member.CreatedAt))

	return nil
}

// membersUpdateAction handles updating a member.
func membersUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	uidStr := cmd.Args().First()
	if uidStr == "" {
		return cli.Exit("Error: member UID is required", 5)
	}

	memberUID, err := uuid.Parse(uidStr)
	if err != nil {
		return cli.Exit("Error: invalid member UID: "+err.Error(), 5)
	}

	req := client.UpdateMemberJSONRequestBody{}
	hasChanges := false

	if cmd.IsSet("role") {
		role := client.UpdateMemberRequestRole(cmd.String("role"))
		req.Role = &role
		hasChanges = true
	}

	if !hasChanges {
		return cli.Exit("Error: at least one field must be specified to update", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateMemberWithResponse(ctx, cliCtx.GetOrg(), memberUID, req)
	if err != nil {
		return cliCtx.HandleError("Failed to update member", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update member", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Member updated successfully: "+uidStr)
	if resp.JSON200.Role != nil {
		output.PrintMessage(os.Stdout, "  Role: "+string(*resp.JSON200.Role))
	}

	return nil
}

// membersRemoveAction handles removing a member from the organization.
func membersRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	uidStr := cmd.Args().First()
	if uidStr == "" {
		return cli.Exit("Error: member UID is required", 5)
	}

	memberUID, err := uuid.Parse(uidStr)
	if err != nil {
		return cli.Exit("Error: invalid member UID: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.RemoveMemberWithResponse(ctx, cliCtx.GetOrg(), memberUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove member", err)
	}

	if resp.StatusCode() != 200 && resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove member", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Success("Member removed successfully: " + uidStr)
	}

	output.PrintSuccess(os.Stdout, "Member removed successfully: "+uidStr)
	return nil
}
