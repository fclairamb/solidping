package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

var (
	// ErrManifestRequired is returned when no manifest file is supplied.
	ErrManifestRequired = errors.New("a manifest file is required (-f/--file)")
	// ErrApplyAborted is returned when the user declines the apply prompt.
	ErrApplyAborted = errors.New("apply aborted")
)

// applyPlanEntry mirrors the server-side ApplyPlanEntry for pretty-printing.
type applyPlanEntry struct {
	Slug         string `json:"slug"`
	PreviousSlug string `json:"previousSlug,omitempty"`
	Action       string `json:"action"`
	Reason       string `json:"reason,omitempty"`
}

// applyResult mirrors the server-side ApplyResult for pretty-printing.
type applyResult struct {
	Manifest  string           `json:"manifest"`
	DryRun    bool             `json:"dryRun"`
	Pruned    bool             `json:"pruned"`
	Created   int              `json:"created"`
	Updated   int              `json:"updated"`
	Deleted   int              `json:"deleted"`
	Unmanaged int              `json:"unmanaged"`
	Plan      []applyPlanEntry `json:"plan"`
	Warnings  []string         `json:"warnings"`
	Errors    []struct {
		Slug  string `json:"slug"`
		Error string `json:"error"`
	} `json:"errors"`
}

// detectContentType picks the request Content-Type from the file extension so
// the server takes the right parse path. Defaults to YAML for hand-authored
// files; JSON only for .json.
func detectContentType(path string) string {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".json") {
		return "application/json"
	}

	return "application/yaml"
}

// printApplyPlan renders the plan as a human-readable table.
func printApplyPlan(res *applyResult) {
	if len(res.Plan) == 0 {
		output.PrintMessage(os.Stdout, "Plan: no changes")

		return
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{"ACTION", colSlug, "REASON"})
	for i := range res.Plan {
		entry := &res.Plan[i]
		slug := entry.Slug
		if entry.PreviousSlug != "" {
			slug = entry.PreviousSlug + " -> " + entry.Slug
		}
		tbl.AppendRow(table.Row{strings.ToUpper(entry.Action), slug, entry.Reason})
	}
	tbl.Render()
}

// printApplySummary prints the counts and any warnings/errors.
func printApplySummary(res *applyResult) {
	prefix := "Applied"
	if res.DryRun {
		prefix = "Plan (dry-run)"
	}

	output.PrintMessage(os.Stdout, fmt.Sprintf(
		"%s: %d created, %d updated, %d deleted, %d unmanaged (manifest: %s)",
		prefix, res.Created, res.Updated, res.Deleted, res.Unmanaged, res.Manifest))

	for _, w := range res.Warnings {
		output.PrintMessage(os.Stdout, "WARNING: "+w)
	}

	for i := range res.Errors {
		output.PrintError(os.Stdout, fmt.Sprintf("  %s: %s", res.Errors[i].Slug, res.Errors[i].Error))
	}
}

// confirmApply prompts the operator before a mutating apply. Returns true to
// proceed. --yes / non-text output skip the prompt.
func confirmApply() bool {
	_, _ = fmt.Fprint(os.Stdout, "Apply these changes? [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))

	return answer == "y" || answer == "yes"
}

// applyAction implements `sp apply -f <manifest> [--dry-run] [--prune] [--yes] [--force]`.
//
//nolint:cyclop,funlen // CLI orchestration: read, plan, confirm, apply
func applyAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	file := cmd.String(flagFile)
	if file == "" {
		if cmd.Args().Len() > 0 {
			file = cmd.Args().Get(0)
		}
	}
	if file == "" {
		return cli.Exit("Error: "+ErrManifestRequired.Error(), 5)
	}

	body, readErr := os.ReadFile(file)
	if readErr != nil {
		return cli.Exit(fmt.Sprintf("Error: cannot read manifest: %v", readErr), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	contentType := detectContentType(file)
	dryRun := cmd.Bool("dry-run")
	prune := cmd.Bool("prune")
	force := cmd.Bool("force")
	assumeYes := cmd.Bool("yes")

	// Step 1: always compute a dry-run plan first so the operator sees the diff.
	planRaw, planErr := apiClient.ApplyChecks(ctx, cliCtx.GetOrg(), body, contentType,
		client.ApplyOptions{DryRun: true, Prune: prune, Force: force})
	if planErr != nil {
		return cliCtx.HandleError("Failed to compute plan", planErr)
	}

	var planRes applyResult
	if jsonErr := json.Unmarshal(planRaw, &planRes); jsonErr != nil {
		return cliCtx.HandleError("Failed to parse plan", jsonErr)
	}

	if cliCtx.IsText() {
		printApplyPlan(&planRes)
		printApplySummary(&planRes)
	}

	// Dry-run mode stops after the plan.
	if dryRun {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.Print(planRes)
		}

		return nil
	}

	// Step 2: confirm unless --yes (text mode only — scripted/JSON callers must
	// pass --yes explicitly).
	if cliCtx.IsText() && !assumeYes {
		if !confirmApply() {
			output.PrintMessage(os.Stdout, ErrApplyAborted.Error())

			return cli.Exit("", 2)
		}
	} else if !cliCtx.IsText() && !assumeYes {
		return cliCtx.Outputter.PrintError(ErrApplyAborted)
	}

	// Step 3: real apply.
	applyRaw, applyErr := apiClient.ApplyChecks(ctx, cliCtx.GetOrg(), body, contentType,
		client.ApplyOptions{DryRun: false, Prune: prune, Force: force})
	if applyErr != nil {
		return cliCtx.HandleError("Failed to apply manifest", applyErr)
	}

	var applyRes applyResult
	if jsonErr := json.Unmarshal(applyRaw, &applyRes); jsonErr != nil {
		return cliCtx.HandleError("Failed to parse apply result", jsonErr)
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(applyRes)
	}

	printApplySummary(&applyRes)
	if len(applyRes.Errors) > 0 {
		return cli.Exit("", 1)
	}

	return nil
}

// checksExportAction implements `sp checks export [--file <f>] [--format yaml|json]`.
func checksExportAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	doc, err := apiClient.ExportChecks(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to export checks", err)
	}

	outFile := cmd.String(flagFile)
	format := exportFormatFromFlags(cmd.String(flagFormat), outFile)

	var rendered []byte

	switch format {
	case "yaml", "yml":
		rendered, err = jsonToYAML(doc)
		if err != nil {
			return cli.Exit(fmt.Sprintf("Error: cannot render yaml: %v", err), 5)
		}
	default:
		// Pretty-print the JSON document.
		rendered = doc

		var pretty interface{}
		if jsonErr := json.Unmarshal(doc, &pretty); jsonErr == nil {
			if formatted, mErr := json.MarshalIndent(pretty, "", "  "); mErr == nil {
				rendered = formatted
			}
		}
	}

	if outFile == "" {
		_, _ = os.Stdout.Write(rendered)
		_, _ = fmt.Fprintln(os.Stdout)

		return nil
	}

	if writeErr := os.WriteFile(outFile, rendered, 0o600); writeErr != nil {
		return cli.Exit(fmt.Sprintf("Error: cannot write %s: %v", outFile, writeErr), 5)
	}

	output.PrintSuccess(os.Stdout, "Checks exported to "+outFile)

	return nil
}

// importResultSummary mirrors the server's checks.ImportResult for
// text-mode reporting.
type importResultSummary struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
	Errors  []struct {
		Index int    `json:"index"`
		Slug  string `json:"slug"`
		Error string `json:"error"`
	} `json:"errors"`
}

// checksImportAction implements `sp checks import <file> [--dry-run]`. The
// file's Content-Type is inferred from its extension (reusing
// detectContentType, the same helper `sp apply` uses) so a hand-authored YAML
// file parses correctly server-side.
func checksImportAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: an export file is required", 5)
	}
	file := cmd.Args().Get(0)

	body, readErr := os.ReadFile(file)
	if readErr != nil {
		return cli.Exit(fmt.Sprintf("Error: cannot read file: %v", readErr), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	contentType := detectContentType(file)
	dryRun := cmd.Bool("dry-run")

	resultRaw, importErr := apiClient.ImportChecks(ctx, cliCtx.GetOrg(), body, contentType, dryRun)
	if importErr != nil {
		return cliCtx.HandleError("Failed to import checks", importErr)
	}

	var result importResultSummary
	if jsonErr := json.Unmarshal(resultRaw, &result); jsonErr != nil {
		return cliCtx.HandleError("Failed to parse import result", jsonErr)
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(result)
	}

	summary := formatImportSummary(result, dryRun)
	output.PrintMessage(os.Stdout, summary.headline)

	if len(summary.errorLines) > 0 {
		output.PrintError(os.Stdout, fmt.Sprintf("%d error(s):", len(summary.errorLines)))
		for _, line := range summary.errorLines {
			output.PrintMessage(os.Stdout, "  "+line)
		}

		return cli.Exit("", summary.exitCode)
	}

	return nil
}

// importSummaryText is the text-mode rendering of an import result: the
// headline created/updated/skipped counts, per-item error lines, and the exit
// code — matching the reference workflow's do_import contract (created=N
// updated=N skipped=N; non-zero exit when any item failed).
type importSummaryText struct {
	headline   string
	errorLines []string
	exitCode   int
}

// formatImportSummary is the pure (network-free) core of checksImportAction's
// text-mode output, split out so the summary/exit-code contract is directly
// unit-testable without a live server.
func formatImportSummary(result importResultSummary, dryRun bool) importSummaryText {
	mode := "IMPORT"
	if dryRun {
		mode = "DRY RUN"
	}

	summary := importSummaryText{
		headline: fmt.Sprintf(
			"%s: created=%d updated=%d skipped=%d", mode, result.Created, result.Updated, result.Skipped),
	}

	for i := range result.Errors {
		e := &result.Errors[i]
		summary.errorLines = append(summary.errorLines, fmt.Sprintf("[%d] %s: %s", e.Index, e.Slug, e.Error))
	}

	if len(summary.errorLines) > 0 {
		summary.exitCode = 1
	}

	return summary
}
