package cli

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Entitlements CLI shared strings.
const (
	flagMaxChecks          = "max-checks"
	flagMaxUsers           = "max-users"
	flagMaxUsersDeprecated = "max-sso-users"
	flagMaxChecksPerMinute = "max-checks-per-minute"
	flagDisplayName        = "display-name"
	flagDisplayEmoji       = "display-emoji"
	flagUsage              = "usage"

	colSource = "SOURCE"

	valUnlimited = "unlimited"

	usageMaxChecks          = "Maximum non-internal checks (omit for unchanged/unlimited)"
	usageMaxUsers           = "Maximum organization members (omit for unchanged/unlimited)"
	usageMaxChecksPerMinute = "Aggregate check dispatch rate per minute"
	usageDisplayName        = "Display plan name (e.g. Team)"
	usageDisplayEmoji       = "Display plan emoji (e.g. 🚀)"

	msgEntitlementsNoFields = "Error: specify at least one field to update " +
		"(--max-checks, --max-users, --max-checks-per-minute, --display-name, --display-emoji)"
)

// entitlementsCommand builds the "entitlements" command group.
func entitlementsCommand() *cli.Command {
	return &cli.Command{
		Name:    "entitlements",
		Aliases: []string{"entitlement", "ent"},
		Usage:   "Manage per-organization entitlement limits",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:  flagGet,
				Usage: "Show the organization's resolved entitlements",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: flagUsage, Usage: "Include current usage figures"},
				},
				Action: entitlementsGetAction,
			},
			{
				Name:   cmdSet,
				Usage:  "Replace the organization's entitlements (unset limits become unlimited)",
				Flags:  entitlementsWriteFlags(),
				Action: entitlementsSetAction,
			},
			{
				Name:   "patch",
				Usage:  "Partially update the organization's entitlements (unset fields unchanged)",
				Flags:  entitlementsWriteFlags(),
				Action: entitlementsPatchAction,
			},
			{
				Name:  "audits",
				Usage: "List entitlement change audit entries",
				Flags: []cli.Flag{
					&cli.IntFlag{Name: flagLimit, Usage: "Maximum results (1-200, default 50)"},
				},
				Action: entitlementsAuditsAction,
			},
		},
	}
}

// entitlementsWriteFlags is the shared flag set for set/patch.
func entitlementsWriteFlags() []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{Name: flagMaxChecks, Usage: usageMaxChecks},
		&cli.IntFlag{Name: flagMaxUsers, Aliases: []string{flagMaxUsersDeprecated}, Usage: usageMaxUsers},
		&cli.IntFlag{Name: flagMaxChecksPerMinute, Usage: usageMaxChecksPerMinute},
		&cli.StringFlag{Name: flagDisplayName, Usage: usageDisplayName},
		&cli.StringFlag{Name: flagDisplayEmoji, Usage: usageDisplayEmoji},
	}
}

// entitlementsGetAction shows the current org's resolved entitlements.
func entitlementsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.GetEntitlementsParams{}
	if cmd.Bool(flagUsage) {
		with := flagUsage
		params.With = &with
	}

	resp, err := apiClient.GetEntitlementsWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to get entitlements", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get entitlements", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderEntitlements(resp.JSON200)

	return nil
}

// entitlementsSetAction replaces the org's entitlements (PUT).
func entitlementsSetAction(ctx context.Context, cmd *cli.Command) error {
	return entitlementsWrite(ctx, cmd, false)
}

// entitlementsPatchAction partially updates the org's entitlements (PATCH).
func entitlementsPatchAction(ctx context.Context, cmd *cli.Command) error {
	return entitlementsWrite(ctx, cmd, true)
}

// entitlementsWrite issues the PUT (partial=false) or PATCH (partial=true) call.
func entitlementsWrite(ctx context.Context, cmd *cli.Command, partial bool) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	body, hasField := buildEntitlementsBody(cmd)
	if !hasField {
		return cli.Exit(msgEntitlementsNoFields, 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	var (
		status  int
		payload *client.EntitlementsResponse
		callErr error
	)

	if partial {
		resp, respErr := apiClient.PatchEntitlementsWithResponse(ctx, cliCtx.GetOrg(), body)
		callErr = respErr
		if resp != nil {
			status, payload = resp.StatusCode(), resp.JSON200
		}
	} else {
		resp, respErr := apiClient.SetEntitlementsWithResponse(ctx, cliCtx.GetOrg(), body)
		callErr = respErr
		if resp != nil {
			status, payload = resp.StatusCode(), resp.JSON200
		}
	}

	if callErr != nil {
		return cliCtx.HandleError("Failed to update entitlements", callErr)
	}

	if status != 200 || payload == nil {
		return cliCtx.HandleStatusError("Failed to update entitlements", status)
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(payload)
	}

	output.PrintSuccess(os.Stdout, "Entitlements updated")
	renderEntitlements(payload)

	return nil
}

// buildEntitlementsBody assembles the request body from flags, reporting whether
// any field was provided.
func buildEntitlementsBody(cmd *cli.Command) (client.SetEntitlementsRequest, bool) {
	limits := client.EntitlementLimits{
		MaxChecks:          optInt(cmd, flagMaxChecks),
		MaxUsers:           optInt(cmd, flagMaxUsers),
		MaxChecksPerMinute: optInt(cmd, flagMaxChecksPerMinute),
	}

	body := client.SetEntitlementsRequest{
		Limits:       &limits,
		DisplayName:  optString(cmd, flagDisplayName),
		DisplayEmoji: optString(cmd, flagDisplayEmoji),
	}

	hasField := cmd.IsSet(flagMaxChecks) || cmd.IsSet(flagMaxUsers) ||
		cmd.IsSet(flagMaxChecksPerMinute) || cmd.IsSet(flagDisplayName) ||
		cmd.IsSet(flagDisplayEmoji)

	return body, hasField
}

// entitlementsAuditsAction lists the org's entitlement audit log.
func entitlementsAuditsAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.ListEntitlementsAuditsParams{Limit: optInt(cmd, flagLimit)}

	resp, err := apiClient.ListEntitlementsAuditsWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list entitlement audits", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list entitlement audits", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No entitlement audits")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colSource, colActor, colReason, colCreated})

	for i := range *resp.JSON200.Data {
		audit := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			audit.Uid,
			audit.Source,
			audit.Actor,
			derefStr(audit.Reason),
			audit.CreatedAt.Format(time.RFC3339),
		})
	}

	tbl.Render()

	return nil
}

// renderEntitlements prints an EntitlementsResponse in text mode.
func renderEntitlements(ent *client.EntitlementsResponse) {
	output.PrintMessage(os.Stdout, "Source:               "+string(ent.Source))
	output.PrintMessage(os.Stdout, "Max checks:           "+limitStr(ent.Limits.MaxChecks))
	output.PrintMessage(os.Stdout, "Max users:            "+limitStr(ent.Limits.MaxUsers))
	output.PrintMessage(os.Stdout, "Max checks/minute:    "+limitStr(ent.Limits.MaxChecksPerMinute))
	output.PrintMessage(os.Stdout, "Display name:         "+derefStr(ent.DisplayName))
	output.PrintMessage(os.Stdout, "Display emoji:        "+derefStr(ent.DisplayEmoji))
	output.PrintMessage(os.Stdout, "Stale:                "+strconv.FormatBool(ent.Stale))

	if ent.UpgradeUrl != nil && *ent.UpgradeUrl != "" {
		output.PrintMessage(os.Stdout, "Upgrade URL:          "+*ent.UpgradeUrl)
	}

	if ent.Usage != nil {
		output.PrintMessage(os.Stdout, "Usage - checks:       "+strconv.Itoa(ent.Usage.Checks))
		output.PrintMessage(os.Stdout, "Usage - checks/min:   "+
			strconv.FormatFloat(ent.Usage.ChecksPerMinute, 'f', -1, 64))
		output.PrintMessage(os.Stdout, "Usage - users:        "+strconv.Itoa(ent.Usage.SsoUsers))
	}
}

// limitStr formats a nullable limit pointer, showing "unlimited" for nil.
func limitStr(v *int) string {
	if v == nil {
		return valUnlimited
	}

	return strconv.Itoa(*v)
}
