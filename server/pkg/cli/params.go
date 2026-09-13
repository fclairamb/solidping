package cli

import (
	"context"
	"os"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Parameter CLI shared strings.
const (
	colKey     = "KEY"
	colSecret  = "SECRET"
	colUpdated = "UPDATED"

	flagPublic = "public"

	argKey = "<key>"

	msgParamKeyRequired = "Error: parameter key is required"

	// secretPlaceholder is what a secret parameter shows in the table. It is a
	// placeholder, not a masked value: the API never sends the value at all.
	secretPlaceholder = "(write-only)"
)

// paramKeyArg reads the parameter key from the first argument.
func paramKeyArg(cmd *cli.Command) (string, error) {
	key := cmd.Args().First()
	if key == "" {
		return "", cli.Exit(msgParamKeyRequired, 5)
	}

	return key, nil
}

// paramValueCell renders one parameter's value column.
func paramValueCell(param *client.OrgParameter) string {
	if param.Secret {
		return secretPlaceholder
	}

	if param.Value == nil {
		return ""
	}

	return *param.Value
}

// paramsListAction lists the organization's parameters.
func paramsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListOrgParametersWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list parameters", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list parameters", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No parameters found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colKey, colValue, colSecret, colUpdated})

	for i := range *resp.JSON200.Data {
		param := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			param.Key,
			paramValueCell(param),
			param.Secret,
			param.UpdatedAt.Format("2006-01-02 15:04"),
		})
	}

	tbl.Render()

	return nil
}

// paramsGetAction shows one parameter. A secret one shows no value — there is
// no endpoint that returns it, by design.
func paramsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	key, err := paramKeyArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetOrgParameterWithResponse(ctx, cliCtx.GetOrg(), key)
	if err != nil {
		return cliCtx.HandleError("Failed to get parameter", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get parameter", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintMessage(os.Stdout, "Key:     "+resp.JSON200.Key)
	output.PrintMessage(os.Stdout, "Value:   "+paramValueCell(resp.JSON200))
	output.PrintMessage(os.Stdout, "Updated: "+resp.JSON200.UpdatedAt.Format("2006-01-02 15:04:05"))

	return nil
}

// paramsSetAction creates a parameter, or rotates an existing one. Same command
// for both: the key — and therefore every ${param:KEY} reference to it — does
// not move when the value does.
func paramsSetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	key, err := paramKeyArg(cmd)
	if err != nil {
		return err
	}

	// The value may be the second positional argument or --value, so a secret
	// can come from a shell that keeps it out of the argument list.
	value := cmd.Args().Get(1)
	if flagged := cmd.String(flagValue); flagged != "" {
		value = flagged
	}

	// Secret by default: a parameter exists to hold something not worth
	// committing. --public opts out, for a plain setting.
	secret := !cmd.Bool(flagPublic)

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.SetOrgParameterWithResponse(ctx, cliCtx.GetOrg(), key,
		client.SetOrgParameterJSONRequestBody{Value: value, Secret: &secret})
	if err != nil {
		return cliCtx.HandleError("Failed to set parameter", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to set parameter", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Set parameter "+key)
	output.PrintMessage(os.Stdout, "Reference it from a check config as ${param:"+key+"}")

	return nil
}

// paramsDeleteAction removes a parameter.
func paramsDeleteAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	key, err := paramKeyArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteOrgParameterWithResponse(ctx, cliCtx.GetOrg(), key)
	if err != nil {
		return cliCtx.HandleError("Failed to delete parameter", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to delete parameter", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Deleted parameter "+key)
	output.PrintMessage(os.Stdout,
		"Any check referencing ${param:"+key+"} will now fail with an unresolved secret reference")

	return nil
}

// paramsCommand builds the "params" command group — the CLI half of the org
// parameters API (spec 2026-09-11-03).
func paramsCommand() *cli.Command {
	return &cli.Command{
		Name:    "params",
		Aliases: []string{"param", "parameters"},
		Usage:   "Manage the organization parameters a check config references as ${param:KEY}",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:   flagList,
				Usage:  "List parameters (secret values are never shown)",
				Action: paramsListAction,
			},
			{
				Name:      flagGet,
				Usage:     "Get one parameter",
				ArgsUsage: argKey,
				Action:    paramsGetAction,
			},
			{
				Name:      cmdSet,
				Usage:     "Create or rotate a parameter",
				ArgsUsage: argKey + " [value]",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagValue, Usage: "Value to store (alternative to the positional argument)"},
					&cli.BoolFlag{
						Name:  flagPublic,
						Usage: "Store a readable value instead of a write-only secret",
					},
				},
				Action: paramsSetAction,
			},
			{
				Name:      flagRemove,
				Aliases:   []string{"rm", cmdDelete},
				Usage:     "Delete a parameter",
				ArgsUsage: argKey,
				Action:    paramsDeleteAction,
			},
		},
	}
}
