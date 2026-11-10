package mcp

import (
	"context"
)

// checkGroupOutputProps documents the group fields agents rely on, verified
// against checkgroups.CheckGroupResponse's JSON tags.
func checkGroupOutputProps() map[string]any {
	return map[string]any{
		propUID:              stringProp("Check group UID."),
		schemaKeyName:        stringProp("Human-readable name."),
		schemaKeySlug:        stringProp("URL-friendly slug."),
		schemaKeyDescription: stringProp("Free-text description, when set."),
		"sortOrder":          intProp("Display order among the org's groups."),
		"checkCount":         intProp("Number of member checks."),
		propStatus: stringProp(
			"Rollup of enabled member checks' statuses, recomputed on every read.",
		),
		"memberStatusCounts": objectProp(
			"Per-status count of enabled member checks, e.g. {\"up\": 3, \"down\": 1}.",
		),
		"escalationPolicyUid": stringProp(
			"Group-level escalation policy member checks inherit, when set.",
		),
		schemaKeyCreatedAt: stringProp("RFC3339 creation timestamp."),
		schemaKeyUpdatedAt: stringProp("RFC3339 last-update timestamp."),
	}
}

func listCheckGroupsDef() ToolDefinition {
	return ToolDefinition{
		Name: toolListCheckGroups,
		Description: "List every check group of the organization in one unpaginated response: " +
			"slug, rolled-up status and check count per group. A group bundles related checks for " +
			"shared incident handling and dashboard layout. Use it to find the checkGroupUid that " +
			"create_check and update_check take, or the group a status page resource pins; use " +
			"list_checks with checkGroupUid for the checks inside a group, and list_check_types for " +
			"the kinds of check you can create. Groups are created and edited in the dashboard, not " +
			"through MCP. Read-only: works with mcp:read tokens.",
		InputSchema:  objectSchema(map[string]any{}, nil),
		OutputSchema: dataOutputSchema("Check groups on this page.", checkGroupOutputProps()),
		Annotations:  readOnlyAnnotations("List check groups"),
	}
}

func (h *Handler) toolListCheckGroups(ctx context.Context, orgSlug string, _ map[string]any) ToolCallResult {
	result, err := h.checkGroupsSvc.ListCheckGroups(ctx, orgSlug)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(map[string]any{schemaKeyData: result})
}
