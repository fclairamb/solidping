package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/urfave/cli/v3"
	"gopkg.in/yaml.v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// diffJSONIndent is the indent used when rendering documents for the diff, so
// it reads like a normal JSON file rather than a single line.
const diffJSONIndent = "  "

// normalizeForDiff decodes a JSON or YAML document (JSON/YAML sniffed the
// same way the server sniffs import bodies), drops the exportedAt timestamp
// (it always differs and never means drift), and renders it as
// sorted-key-indented JSON lines — matching the reference workflow's
// `json.dumps(doc, indent=1, sort_keys=True).splitlines()`. Go's
// encoding/json already sorts map[string]any keys alphabetically, so decoding
// into a generic map gives us that for free.
func normalizeForDiff(raw []byte) ([]string, error) {
	generic, err := decodeGenericDocument(raw)
	if err != nil {
		return nil, err
	}

	delete(generic, "exportedAt")

	rendered, err := json.MarshalIndent(generic, "", diffJSONIndent)
	if err != nil {
		return nil, fmt.Errorf("render document: %w", err)
	}

	return strings.Split(string(rendered), "\n"), nil
}

// decodeGenericDocument decodes raw as JSON if it looks like JSON (or the
// unmarshal succeeds), otherwise as YAML.
func decodeGenericDocument(raw []byte) (map[string]any, error) {
	trimmed := strings.TrimLeft(string(raw), " \t\r\n")

	var generic map[string]any
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal(raw, &generic); err != nil {
			return nil, fmt.Errorf("parse json document: %w", err)
		}

		return generic, nil
	}

	if err := yaml.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("parse yaml document: %w", err)
	}

	return normalizeYAMLMap(generic), nil
}

// normalizeYAMLMap recursively converts the map[interface{}]interface{} /
// map[string]interface{} mix yaml.v3 can produce into a pure
// map[string]any tree so json.Marshal renders it (and sorts its keys)
// without error.
func normalizeYAMLMap(value any) map[string]any {
	out, _ := normalizeYAMLValue(value).(map[string]any)

	return out
}

func normalizeYAMLValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, val := range typed {
			out[k] = normalizeYAMLValue(val)
		}

		return out
	case map[any]any:
		out := make(map[string]any, len(typed))
		for k, val := range typed {
			out[fmt.Sprintf("%v", k)] = normalizeYAMLValue(val)
		}

		return out
	case []any:
		out := make([]any, len(typed))
		for i, val := range typed {
			out[i] = normalizeYAMLValue(val)
		}

		return out
	default:
		return value
	}
}

// diffOutcome is the pure result of comparing a local document against the
// live export: whether it drifted, and (when it did) the rendered unified
// diff text. Kept separate from any I/O so the 0/1/>=2 exit-code contract is
// directly unit-testable, the same way formatImportSummary factors out
// import's summary/exit-code contract.
type diffOutcome struct {
	Drift bool
	Delta string
}

// computeDiffOutcome normalizes both documents (JSON or YAML, exportedAt
// stripped, sorted-key JSON lines) and, when they differ, renders a unified
// diff. Pure: no I/O, no network — everything checksDiffAction needs to
// decide what to print and which exit code to use.
func computeDiffOutcome(localRaw, liveRaw []byte, fromFile string) (diffOutcome, error) {
	localLines, err := normalizeForDiff(localRaw)
	if err != nil {
		return diffOutcome{}, fmt.Errorf("%s: %w", fromFile, err)
	}

	liveLines, err := normalizeForDiff(liveRaw)
	if err != nil {
		return diffOutcome{}, fmt.Errorf("cannot parse live export: %w", err)
	}

	if equalLines(localLines, liveLines) {
		return diffOutcome{Drift: false}, nil
	}

	delta, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        localLines,
		B:        liveLines,
		FromFile: fromFile,
		ToFile:   "solidping (live)",
		Context:  3,
	})
	if err != nil {
		return diffOutcome{}, fmt.Errorf("cannot compute diff: %w", err)
	}

	return diffOutcome{Drift: true, Delta: delta}, nil
}

// diffExitCode maps a diff outcome (and any error from computing it) to the
// CI-friendly contract Proposal 3 requires: 0 = no drift, 1 = drift,
// >=2 = errors.
const diffExitCodeError = 2

func diffExitCode(outcome diffOutcome, err error) int {
	switch {
	case err != nil:
		return diffExitCodeError
	case outcome.Drift:
		return 1
	default:
		return 0
	}
}

// planDrift reports whether a reconcile plan says the file and the instance
// disagree. `unmanaged` is deliberately NOT drift: the slug exists and the
// manifest describes it, it simply is not owned by this manifest yet — it is
// surfaced in the table, never in the exit code.
func planDrift(res *applyResult) bool {
	return res.Created+res.Updated+res.Deleted > 0
}

// reportDiffPlan renders a server-computed reconcile plan as the answer to
// "does this file match?" — the question `sp checks diff` exists for.
func reportDiffPlan(cliCtx *Context, file string, res *applyResult) error {
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(map[string]any{
			"drift": planDrift(res), "file": file, "plan": res,
		})
	}

	printApplyPlan(res)
	printApplySummary(res)

	if !planDrift(res) {
		output.PrintSuccess(os.Stdout, fmt.Sprintf(
			"No drift: %s matches SolidPing (%d unchanged)", file, res.Unchanged))

		return nil
	}

	output.PrintError(os.Stdout, fmt.Sprintf("Drift: %s does not match SolidPing", file))

	return cli.Exit("", 1)
}

// checksDiffAction implements `sp checks diff <file>`.
//
// It asks the SERVER for the reconcile plan (a dry run that mutates nothing)
// and reports its counts: `created=0 updated=0 deleted=0` is the machine-
// readable "the file matches the instance" (spec 2026-09-11-04). Before that
// the answer had to be reconstructed client-side from a textual diff, because
// a dry-run import counted every matched slug as an update — so every external
// tool grew its own normalizer, and every one of them drifted.
//
// The textual diff remains, as `--text` and as the automatic fallback when the
// plan cannot be computed (the plan is admin-only, and an older server does not
// report `unchanged` at all).
//
// Exit 0 (no drift) / 1 (drift) / >=2 (errors) — unchanged, CI-friendly.
//
//nolint:cyclop // one linear fallback chain: plan, then text diff
func checksDiffAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: a local export file is required", 5)
	}
	file := cmd.Args().Get(0)

	localRaw, readErr := os.ReadFile(file)
	if readErr != nil {
		return cli.Exit(fmt.Sprintf("Error: cannot read %s: %v", file, readErr), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	if !cmd.Bool("text") {
		// Prune + force on a DRY RUN: nothing is written either way, and both
		// flags only widen what the plan is allowed to report — without prune a
		// managed check the file no longer carries would be invisible, and
		// without force the deletion cap would refuse to answer at all.
		planRaw, planErr := apiClient.ApplyChecks(ctx, cliCtx.GetOrg(), localRaw, detectContentType(file),
			client.ApplyOptions{DryRun: true, Prune: true, Force: true})
		if planErr == nil {
			var planRes applyResult
			if json.Unmarshal(planRaw, &planRes) == nil {
				return reportDiffPlan(cliCtx, file, &planRes)
			}
		}
	}

	liveRaw, exportErr := apiClient.ExportChecks(ctx, cliCtx.GetOrg())
	if exportErr != nil {
		return cliCtx.HandleError("Failed to export checks", exportErr)
	}

	outcome, computeErr := computeDiffOutcome(localRaw, liveRaw, file)
	exitCode := diffExitCode(outcome, computeErr)

	if computeErr != nil {
		return cli.Exit(fmt.Sprintf("Error: %v", computeErr), exitCode)
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(map[string]any{"drift": outcome.Drift, "file": file})
	}

	if !outcome.Drift {
		output.PrintSuccess(os.Stdout, fmt.Sprintf("No drift: %s matches SolidPing", file))

		return nil
	}

	_, _ = os.Stdout.WriteString(outcome.Delta)
	output.PrintError(os.Stdout, fmt.Sprintf("Drift: %s does not match SolidPing", file))

	return cli.Exit("", exitCode)
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
