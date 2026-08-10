package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"
	"gopkg.in/yaml.v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// errSetFromMissing is returned when sp checks deps set is invoked without --from.
var errSetFromMissing = errors.New("--from <file> is required")

// dependency kind constants.
const (
	depKindHard      = "hard"
	depKindSoft      = "soft"
	depsSetConfigKey = "config"
)

// checksDepsListAction prints the parents (and children) of a check.
func checksDepsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: check uid or slug is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 3)
	}

	deps, err := apiClient.ListCheckDependencies(ctx, cliCtx.GetOrg(), cmd.Args().Get(0))
	if err != nil {
		output.PrintError(os.Stdout, fmt.Sprintf("Failed to list dependencies: %v", err))
		return cli.Exit("", 1)
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(deps)
	}

	if len(deps.DependsOn) == 0 {
		output.PrintMessage(os.Stdout, "No dependencies")
		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{"PARENT_SLUG", "PARENT_NAME", "KIND", "EDGE_UID"})

	for i := range deps.DependsOn {
		edge := &deps.DependsOn[i]
		tbl.AppendRow(table.Row{
			edge.ParentCheck.Slug,
			edge.ParentCheck.Name,
			edge.Kind,
			edge.UID,
		})
	}

	tbl.Render()

	return nil
}

// resolveCheckUID looks up a check by uid or slug and returns its uid.
func resolveCheckUID(ctx context.Context, apiClient *client.SolidPingClient, org, ident string) (string, error) {
	resp, err := apiClient.GetCheckWithResponse(ctx, org, ident, nil)
	if err != nil {
		return "", fmt.Errorf("get check %s: %w", ident, err)
	}

	if resp.JSON200 == nil || resp.JSON200.Uid == nil {
		return "", fmt.Errorf("%w: check %s not found", ErrCheckNotFound, ident)
	}

	return resp.JSON200.Uid.String(), nil
}

// checksDepsAddAction creates one parent edge.
func checksDepsAddAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 2 {
		return cli.Exit("Error: <child-slug> <parent-slug> are required", 5)
	}

	child := cmd.Args().Get(0)
	parent := cmd.Args().Get(1)
	kind := cmd.String("kind")

	if kind == "" {
		kind = depKindHard
	}

	if kind != depKindHard && kind != depKindSoft {
		return cli.Exit("Error: --kind must be 'hard' or 'soft'", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 3)
	}

	parentUID, err := resolveCheckUID(ctx, apiClient, cliCtx.GetOrg(), parent)
	if err != nil {
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 1)
	}

	body := client.CreateDependencyBody{
		ParentCheckUID: parentUID,
		Kind:           kind,
	}

	if desc := cmd.String(flagDescription); desc != "" {
		body.Description = &desc
	}

	edge, err := apiClient.AddCheckDependency(ctx, cliCtx.GetOrg(), child, body)
	if err != nil {
		output.PrintError(os.Stdout, fmt.Sprintf("Failed to add dependency: %v", err))
		return cli.Exit("", 1)
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(edge)
	}

	output.PrintSuccess(os.Stdout, fmt.Sprintf("Added %s → %s (%s)", child, parent, kind))

	return nil
}

// checksDepsRemoveAction drops one edge by parent slug.
func checksDepsRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 2 {
		return cli.Exit("Error: <child-slug> <parent-slug> are required", 5)
	}

	child := cmd.Args().Get(0)
	parent := cmd.Args().Get(1)

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 3)
	}

	deps, err := apiClient.ListCheckDependencies(ctx, cliCtx.GetOrg(), child)
	if err != nil {
		output.PrintError(os.Stdout, fmt.Sprintf("Failed to list dependencies: %v", err))
		return cli.Exit("", 1)
	}

	var depUID string

	for i := range deps.DependsOn {
		edge := &deps.DependsOn[i]
		if edge.ParentCheck.Slug == parent || edge.ParentCheck.UID == parent {
			depUID = edge.UID
			break
		}
	}

	if depUID == "" {
		output.PrintError(os.Stdout, fmt.Sprintf("No dependency on %s found for %s", parent, child))
		return cli.Exit("", 1)
	}

	if err := apiClient.DeleteCheckDependency(ctx, cliCtx.GetOrg(), child, depUID); err != nil {
		output.PrintError(os.Stdout, fmt.Sprintf("Failed to remove dependency: %v", err))
		return cli.Exit("", 1)
	}

	output.PrintSuccess(os.Stdout, fmt.Sprintf("Removed %s → %s", child, parent))

	return nil
}

// checksDepsUpdateAction PATCHes a single edge's kind/description.
//
//nolint:funlen,cyclop // CLI parameter parsing and edge lookup
func checksDepsUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 2 {
		return cli.Exit("Error: <child-slug> <parent-slug> are required", 5)
	}

	child := cmd.Args().Get(0)
	parent := cmd.Args().Get(1)

	if !cmd.IsSet("kind") && !cmd.IsSet(flagDescription) {
		return cli.Exit("Error: at least one of --kind or --description is required", 5)
	}

	kind := cmd.String("kind")
	if cmd.IsSet("kind") && kind != depKindHard && kind != depKindSoft {
		return cli.Exit("Error: --kind must be 'hard' or 'soft'", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 3)
	}

	deps, err := apiClient.ListCheckDependencies(ctx, cliCtx.GetOrg(), child)
	if err != nil {
		output.PrintError(os.Stdout, fmt.Sprintf("Failed to list dependencies: %v", err))
		return cli.Exit("", 1)
	}

	var depUID string

	for i := range deps.DependsOn {
		edge := &deps.DependsOn[i]
		if edge.ParentCheck.Slug == parent || edge.ParentCheck.UID == parent {
			depUID = edge.UID
			break
		}
	}

	if depUID == "" {
		output.PrintError(os.Stdout, fmt.Sprintf("No dependency on %s found for %s", parent, child))
		return cli.Exit("", 1)
	}

	edgeUID, err := uuid.Parse(depUID)
	if err != nil {
		output.PrintError(os.Stdout, fmt.Sprintf("Invalid edge UID %s: %v", depUID, err))
		return cli.Exit("", 1)
	}

	body := client.UpdateCheckDependencyJSONRequestBody{}
	if cmd.IsSet("kind") {
		kindEnum := client.UpdateDependencyRequestKind(kind)
		body.Kind = &kindEnum
	}
	if cmd.IsSet(flagDescription) {
		desc := cmd.String(flagDescription)
		body.Description = &desc
	}

	resp, err := apiClient.UpdateCheckDependencyWithResponse(ctx, cliCtx.GetOrg(), child, edgeUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to update dependency", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update dependency", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, fmt.Sprintf("Updated %s → %s", child, parent))

	return nil
}

// checksDepsGraphAction prints the org-wide dependency graph.
func checksDepsGraphAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 3)
	}

	resp, err := apiClient.GetOrgDependencyGraphWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to get dependency graph", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get dependency graph", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	data := resp.JSON200.Data
	if data == nil || data.Edges == nil || len(*data.Edges) == 0 {
		output.PrintMessage(os.Stdout, "No dependency edges")
		return nil
	}

	// Build a uid -> slug map from the nodes for readable output.
	slugByUID := map[string]string{}
	if data.Nodes != nil {
		for i := range *data.Nodes {
			node := &(*data.Nodes)[i]
			slugByUID[node.Uid.String()] = node.Slug
		}
	}

	labelFor := func(uid string) string {
		if slug, ok := slugByUID[uid]; ok && slug != "" {
			return slug
		}
		return uid
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{"CHILD", "PARENT", "KIND", "EDGE_UID"})

	for i := range *data.Edges {
		edge := &(*data.Edges)[i]
		tbl.AppendRow(table.Row{
			labelFor(edge.ChildCheckUid.String()),
			labelFor(edge.ParentCheckUid.String()),
			string(edge.Kind),
			edge.Uid.String(),
		})
	}

	tbl.Render()

	return nil
}

// depsSetEntry is one row in the YAML/JSON file accepted by `sp checks deps set --from`.
type depsSetEntry struct {
	ParentSlug  string  `json:"parentSlug"            yaml:"parentSlug"`
	Kind        string  `json:"kind"                  yaml:"kind"`
	Description *string `json:"description,omitempty" yaml:"description,omitempty"`
}

// depsSetFile is the top-level shape of the file passed to `--from`.
type depsSetFile struct {
	DependsOn []depsSetEntry `json:"dependsOn" yaml:"dependsOn"`
}

// loadDepsSetFile parses the --from file as YAML (default) or JSON (.json suffix).
func loadDepsSetFile(path string) (*depsSetFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var parsed depsSetFile

	if strings.HasSuffix(strings.ToLower(path), ".json") {
		if jsonErr := json.Unmarshal(raw, &parsed); jsonErr != nil {
			return nil, fmt.Errorf("invalid JSON: %w", jsonErr)
		}
	} else {
		if yamlErr := yaml.Unmarshal(raw, &parsed); yamlErr != nil {
			return nil, fmt.Errorf("invalid YAML: %w", yamlErr)
		}
	}

	return &parsed, nil
}

// buildUpsertBody assembles the PUT-by-slug payload preserving the existing
// check's required fields and overlaying dependsOn.
func buildUpsertBody(existing *client.Check, deps []depsSetEntry) map[string]any {
	body := map[string]any{
		depsSetConfigKey: existing.Config,
		"dependsOn":      deps,
	}

	if existing.Type != nil {
		body["type"] = *existing.Type
	}
	if existing.Name != nil {
		body["name"] = *existing.Name
	}
	if existing.Period != nil {
		body["period"] = *existing.Period
	}
	if existing.Enabled != nil {
		body["enabled"] = *existing.Enabled
	}

	return body
}

// checksDepsSetAction replaces the full set of edges for a child via PUT-by-slug.
func checksDepsSetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: <child-slug> is required", 5)
	}

	child := cmd.Args().Get(0)

	from := cmd.String("from")
	if from == "" {
		return cli.Exit("Error: "+errSetFromMissing.Error(), 5)
	}

	parsed, err := loadDepsSetFile(from)
	if err != nil {
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 1)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 3)
	}

	// PUT-by-slug needs the existing config to preserve type/url/etc; fetch first.
	existing, err := apiClient.GetCheckWithResponse(ctx, cliCtx.GetOrg(), child, nil)
	if err != nil {
		output.PrintError(os.Stdout, fmt.Sprintf("Failed to load check %s: %v", child, err))
		return cli.Exit("", 1)
	}

	if existing.JSON200 == nil {
		output.PrintError(os.Stdout, fmt.Sprintf("Check %s not found", child))
		return cli.Exit("", 1)
	}

	body := buildUpsertBody(existing.JSON200, parsed.DependsOn)

	status, err := apiClient.RawPutCheckBySlug(ctx, cliCtx.GetOrg(), child, body)
	if err != nil {
		output.PrintError(os.Stdout, fmt.Sprintf("Failed to apply dependencies: %v", err))
		return cli.Exit("", 1)
	}

	if status != 200 && status != 201 {
		output.PrintError(os.Stdout, fmt.Sprintf("Unexpected status %d", status))
		return cli.Exit("", 1)
	}

	output.PrintSuccess(os.Stdout, fmt.Sprintf("Replaced %d dependency edge(s) on %s", len(parsed.DependsOn), child))

	return nil
}
