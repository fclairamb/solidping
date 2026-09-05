package cli

import (
	"context"
	"os"
	"strconv"

	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Org CLI shared strings.
const (
	flagSlug               = "slug"
	flagEmailPattern       = "registration-email-pattern"
	flagSessionMaxDuration = "session-max-duration-seconds"

	usageEmailPattern = "Auto-join email regex; pass an empty string to clear it"
	usageSessionMax   = "Session max duration override in seconds; <=0 clears the override"
)

// orgsCommand builds the "orgs" command group (create + settings).
func orgsCommand() *cli.Command {
	return &cli.Command{
		Name:    "orgs",
		Aliases: []string{"org"},
		Usage:   "Manage organizations",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:  cmdCreate,
				Usage: "Create a new organization",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagName, Usage: usageHumanReadableName, Required: true},
					&cli.StringFlag{
						Name:  flagSlug,
						Usage: "URL-friendly slug (3-20 chars, lowercase alphanumeric with hyphens); derived from --name when omitted",
					},
				},
				Action: orgsCreateAction,
			},
			{
				Name:  "settings",
				Usage: "Manage organization settings",
				Commands: []*cli.Command{
					{
						Name:   flagGet,
						Usage:  "Show organization settings",
						Action: orgSettingsGetAction,
					},
					{
						Name:  cmdSet,
						Usage: "Update organization settings",
						Flags: []cli.Flag{
							&cli.StringFlag{Name: flagEmailPattern, Usage: usageEmailPattern},
							&cli.IntFlag{Name: flagSessionMaxDuration, Usage: usageSessionMax},
						},
						Action: orgSettingsSetAction,
					},
				},
			},
		},
	}
}

// orgsCreateAction creates a new organization. This is not org-scoped: the
// caller's token authenticates the request and the new org becomes their own.
func orgsCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	body := client.CreateOrgJSONRequestBody{
		Name: cmd.String(flagName),
	}

	// --slug is optional end to end now: left off, the server derives one from
	// the name. Sending "" would be a *request* for the empty slug, so omit the
	// field entirely instead.
	if slug := cmd.String(flagSlug); slug != "" {
		body.Slug = &slug
	}

	resp, err := apiClient.CreateOrgWithResponse(ctx, body)
	if err != nil {
		return cliCtx.HandleError("Failed to create organization", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create organization", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	org := resp.JSON201
	output.PrintSuccess(os.Stdout, "Created organization "+org.Slug)
	output.PrintMessage(os.Stdout, "  UID:  "+org.Uid.String())
	output.PrintMessage(os.Stdout, "  Name: "+org.Name)
	output.PrintMessage(os.Stdout, "  Slug: "+org.Slug)

	return nil
}

// orgSettingsGetAction shows the current org's settings.
func orgSettingsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetOrgSettingsWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to get organization settings", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get organization settings", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderOrgSettings(resp.JSON200)

	return nil
}

// orgSettingsSetAction updates one or more org settings fields.
func orgSettingsSetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	body := client.UpdateOrgSettingsJSONRequestBody{
		RegistrationEmailPattern:  optString(cmd, flagEmailPattern),
		SessionMaxDurationSeconds: optInt(cmd, flagSessionMaxDuration),
	}

	if body.RegistrationEmailPattern == nil && body.SessionMaxDurationSeconds == nil {
		return cli.Exit("Error: at least one field must be specified to update", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateOrgSettingsWithResponse(ctx, cliCtx.GetOrg(), body)
	if err != nil {
		return cliCtx.HandleError("Failed to update organization settings", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update organization settings", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Organization settings updated")
	renderOrgSettings(resp.JSON200)

	return nil
}

// renderOrgSettings prints an OrgSettingsResponse in text mode.
func renderOrgSettings(settings *client.OrgSettingsResponse) {
	pattern := settings.RegistrationEmailPattern
	if pattern == "" {
		pattern = "(unset)"
	}

	output.PrintMessage(os.Stdout, "Registration email pattern: "+pattern)

	if settings.SessionMaxDurationSeconds != nil {
		output.PrintMessage(os.Stdout,
			"Session max duration:       "+strconv.Itoa(*settings.SessionMaxDurationSeconds)+"s")
	} else {
		output.PrintMessage(os.Stdout, "Session max duration:       (inherited)")
	}
}
