package cli

import (
	"context"
	"encoding/json"
	"os"
	"strconv"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Escalation-policy CLI shared strings.
const (
	colRepeatMax = "REPEAT-MAX"
	colSteps     = "STEPS"
	colDelay     = "DELAY(s)"
	colTargets   = "TARGETS"

	flagRepeatMax   = "repeat-max"
	flagRepeatAfter = "repeat-after"
	flagStepsJSON   = "steps-json"

	usageStepsJSON = `Steps as a JSON array, e.g. ` +
		`'[{"delaySeconds":0,"targets":[{"type":"all_admins"}]}]'`

	msgPolicyUIDRequired = "Error: escalation policy uid is required"
)

// escalationPolicyUIDArg reads and validates the policy UID from the first arg.
func escalationPolicyUIDArg(cmd *cli.Command) (openapi_types.UUID, error) {
	raw := cmd.Args().First()
	if raw == "" {
		return uuid.Nil, cli.Exit(msgPolicyUIDRequired, 5)
	}

	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, cli.Exit("Error: invalid escalation policy uid: "+err.Error(), 5)
	}

	return parsed, nil
}

// parseStepsJSON parses the --steps-json flag into a slice of step inputs when
// set, else returns nil so the field is omitted from the request.
func parseStepsJSON(cmd *cli.Command) (*[]client.EscalationStepInput, error) {
	if !cmd.IsSet(flagStepsJSON) {
		return nil, nil //nolint:nilnil // absent optional steps is represented by a nil pointer
	}

	var steps []client.EscalationStepInput
	if err := json.Unmarshal([]byte(cmd.String(flagStepsJSON)), &steps); err != nil {
		return nil, cli.Exit("Error: invalid --steps-json: "+err.Error(), 5)
	}

	return &steps, nil
}

// renderEscalationPolicy prints a single policy (with steps) in text mode.
func renderEscalationPolicy(policy *client.EscalationPolicy) {
	output.PrintMessage(os.Stdout, "UID:         "+policy.Uid.String())
	output.PrintMessage(os.Stdout, "Name:        "+policy.Name)
	output.PrintMessage(os.Stdout, "Repeat max:  "+strconv.Itoa(policy.RepeatMax))

	if policy.RepeatAfterSeconds != nil {
		output.PrintMessage(os.Stdout, "Repeat after:"+strconv.Itoa(*policy.RepeatAfterSeconds)+"s")
	}

	if policy.Description != nil && *policy.Description != "" {
		output.PrintMessage(os.Stdout, "Description: "+*policy.Description)
	}

	if policy.Steps == nil || len(*policy.Steps) == 0 {
		return
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colPosition, colDelay, colTargets})

	for i := range *policy.Steps {
		step := &(*policy.Steps)[i]
		tbl.AppendRow(table.Row{
			strconv.Itoa(step.Position),
			strconv.Itoa(step.DelaySeconds),
			strconv.Itoa(countTargets(step)),
		})
	}

	tbl.Render()
}

func countTargets(step *client.EscalationStep) int {
	if step.Targets == nil {
		return 0
	}

	return len(*step.Targets)
}

// escalationPoliciesListAction lists all escalation policies for the org.
func escalationPoliciesListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListEscalationPoliciesWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list escalation policies", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list escalation policies", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No escalation policies found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colName, colRepeatMax, colSteps})

	for i := range *resp.JSON200.Data {
		policy := &(*resp.JSON200.Data)[i]

		stepCount := 0
		if policy.Steps != nil {
			stepCount = len(*policy.Steps)
		}

		tbl.AppendRow(table.Row{
			policy.Uid.String(),
			policy.Name,
			strconv.Itoa(policy.RepeatMax),
			strconv.Itoa(stepCount),
		})
	}

	tbl.Render()

	return nil
}

// escalationPoliciesCreateAction creates a new escalation policy.
func escalationPoliciesCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	name := cmd.String(flagName)
	if name == "" {
		return cli.Exit("Error: --name is required", 5)
	}

	steps, err := parseStepsJSON(cmd)
	if err != nil {
		return err
	}

	body := client.CreateEscalationPolicyJSONRequestBody{
		Name:               name,
		Description:        optString(cmd, flagDescription),
		RepeatMax:          optInt(cmd, flagRepeatMax),
		RepeatAfterSeconds: optInt(cmd, flagRepeatAfter),
		Steps:              steps,
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateEscalationPolicyWithResponse(ctx, cliCtx.GetOrg(), body)
	if err != nil {
		return cliCtx.HandleError("Failed to create escalation policy", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create escalation policy", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Created escalation policy "+resp.JSON201.Uid.String())
	renderEscalationPolicy(resp.JSON201)

	return nil
}

// escalationPoliciesGetAction fetches a single escalation policy by UID.
func escalationPoliciesGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	policyUID, err := escalationPolicyUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetEscalationPolicyWithResponse(ctx, cliCtx.GetOrg(), policyUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get escalation policy", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get escalation policy", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderEscalationPolicy(resp.JSON200)

	return nil
}

// escalationPoliciesUpdateAction patches an escalation policy.
func escalationPoliciesUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	policyUID, err := escalationPolicyUIDArg(cmd)
	if err != nil {
		return err
	}

	steps, err := parseStepsJSON(cmd)
	if err != nil {
		return err
	}

	body := client.UpdateEscalationPolicyJSONRequestBody{
		Name:               optString(cmd, flagName),
		Description:        optString(cmd, flagDescription),
		RepeatMax:          optInt(cmd, flagRepeatMax),
		RepeatAfterSeconds: optInt(cmd, flagRepeatAfter),
		Steps:              steps,
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateEscalationPolicyWithResponse(ctx, cliCtx.GetOrg(), policyUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to update escalation policy", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update escalation policy", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Updated escalation policy "+resp.JSON200.Uid.String())
	renderEscalationPolicy(resp.JSON200)

	return nil
}

// escalationPoliciesRemoveAction deletes an escalation policy by UID.
func escalationPoliciesRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	policyUID, err := escalationPolicyUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteEscalationPolicyWithResponse(ctx, cliCtx.GetOrg(), policyUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove escalation policy", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove escalation policy", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Removed escalation policy "+policyUID.String())

	return nil
}
