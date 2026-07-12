package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"
	"gopkg.in/yaml.v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// errValidateInputEmpty is returned when neither a file nor stdin provides a payload.
var errValidateInputEmpty = errors.New("no check definition provided (use --file or pipe via stdin)")

// defaultAvailabilityPeriods is the period set used when --periods is omitted.
const defaultAvailabilityPeriods = "24h,7d,30d,90d"

// readCheckDefinition reads a check definition (type/slug/config/dependsOn) from
// --file (or the first positional arg), falling back to stdin when neither is
// given. Accepts JSON or YAML; YAML is round-tripped through JSON so the
// generated struct's json tags (e.g. dependsOn) resolve correctly.
func readCheckDefinition(cmd *cli.Command) (client.ValidateCheckRequest, error) {
	var req client.ValidateCheckRequest

	file := cmd.String(flagFile)
	if file == "" && cmd.Args().Len() > 0 {
		file = cmd.Args().Get(0)
	}

	var (
		raw []byte
		err error
	)

	if file == "" || file == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(file)
	}

	if err != nil {
		return req, fmt.Errorf("read check definition: %w", err)
	}

	if len(raw) == 0 {
		return req, errValidateInputEmpty
	}

	// Parse into a generic map first (JSON, then YAML), then re-encode to JSON so
	// the typed struct's json tags apply regardless of the source format.
	var generic map[string]any
	if jsonErr := json.Unmarshal(raw, &generic); jsonErr != nil {
		if yamlErr := yaml.Unmarshal(raw, &generic); yamlErr != nil {
			return req, fmt.Errorf("parse check definition (JSON/YAML): %w", yamlErr)
		}
	}

	normalized, err := json.Marshal(generic)
	if err != nil {
		return req, fmt.Errorf("normalize check definition: %w", err)
	}

	if err := json.Unmarshal(normalized, &req); err != nil {
		return req, fmt.Errorf("decode check definition: %w", err)
	}

	return req, nil
}

// checksValidateAction implements `sp checks validate [--file <f>]` (or stdin).
func checksValidateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	req, err := readCheckDefinition(cmd)
	if err != nil {
		return cli.Exit("Error: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ValidateCheckWithResponse(ctx, cliCtx.GetOrg(), req)
	if err != nil {
		return cliCtx.HandleError("Failed to validate check", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to validate check", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	result := resp.JSON200
	if result.Valid {
		output.PrintSuccess(os.Stdout, "Check configuration is valid")

		return nil
	}

	output.PrintError(os.Stdout, "Check configuration is invalid")

	if result.Fields != nil {
		fields := *result.Fields
		for i := range fields {
			output.PrintMessage(os.Stdout, "  "+fields[i].Name+": "+fields[i].Message)
		}
	}

	return cli.Exit("", 1)
}

// checksCloneAction implements `sp checks clone <uid|slug>`.
//
//nolint:funlen // CLI parameter parsing and response formatting
func checksCloneAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: check uid or slug is required", 5)
	}

	source := cmd.Args().Get(0)

	body := client.CloneCheckJSONRequestBody{}
	if cmd.IsSet(flagName) {
		name := cmd.String(flagName)
		body.Name = &name
	}
	if cmd.IsSet(keySlug) {
		slug := cmd.String(keySlug)
		body.Slug = &slug
	}
	if cmd.IsSet(flagDescription) {
		desc := cmd.String(flagDescription)
		body.Description = &desc
	}
	if cmd.IsSet("group") {
		group := cmd.String("group")
		body.CheckGroupUid = &group
	}
	if cmd.IsSet("enabled") {
		enabled := cmd.Bool("enabled")
		body.Enabled = &enabled
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CloneCheckWithResponse(ctx, cliCtx.GetOrg(), source, body)
	if err != nil {
		return cliCtx.HandleError("Failed to clone check", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to clone check", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	clone := resp.JSON201

	slug := ""
	if clone.Slug != nil {
		slug = *clone.Slug
	}

	uid := ""
	if clone.Uid != nil {
		uid = clone.Uid.String()
	}

	output.PrintSuccess(os.Stdout, fmt.Sprintf("Cloned check %s → %s (%s)", source, slug, uid))

	return nil
}

// checksAvailabilityAction implements `sp checks availability <uid|slug>`.
func checksAvailabilityAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: check uid or slug is required", 5)
	}

	check := cmd.Args().Get(0)

	params := &client.GetCheckAvailabilityParams{
		Periods: defaultAvailabilityPeriods,
	}
	if periods := cmd.String("periods"); periods != "" {
		params.Periods = periods
	}
	if tz := cmd.String("tz"); tz != "" {
		params.Tz = &tz
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetCheckAvailabilityWithResponse(ctx, cliCtx.GetOrg(), check, params)
	if err != nil {
		return cliCtx.HandleError("Failed to get availability", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get availability", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No availability data")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colPeriod, "AVAILABILITY", "PROBES", "DOWNTIME"})

	for i := range *resp.JSON200.Data {
		period := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			derefStr(period.Period),
			formatAvailabilityPct(period.AvailabilityPct),
			formatProbes(period.SuccessfulChecks, period.TotalChecks),
			formatDowntime(period.DowntimeSeconds),
		})
	}

	tbl.Render()

	return nil
}

// derefStr returns the pointed-to string, or "" when nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// formatAvailabilityPct renders an availability percentage, or "n/a" when nil.
func formatAvailabilityPct(pct *float32) string {
	if pct == nil {
		return "n/a"
	}

	return fmt.Sprintf("%.3f%%", *pct)
}

// formatProbes renders "successful/total" probe counts.
func formatProbes(successful, total *int) string {
	succ, tot := 0, 0
	if successful != nil {
		succ = *successful
	}
	if total != nil {
		tot = *total
	}

	return fmt.Sprintf("%d/%d", succ, tot)
}

// formatDowntime renders a downtime-seconds value as a short duration.
func formatDowntime(seconds *int64) string {
	if seconds == nil || *seconds == 0 {
		return "0s"
	}

	return formatDuration(time.Duration(*seconds) * time.Second)
}
