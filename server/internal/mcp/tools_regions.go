package mcp

import (
	"context"
)

// regionOutputProps documents the region fields agents rely on, verified
// against regions.RegionResponse's JSON tags.
func regionOutputProps() map[string]any {
	return map[string]any{
		schemaKeySlug: stringProp(
			"Region slug to pass in create_check/update_check; private regions are \"@\"-prefixed.",
		),
		"emoji":       stringProp("Region emoji shown in the UI."),
		schemaKeyName: stringProp("Human-readable region label."),
		"private": boolProp(
			"Whether the region is an org-private location served by deported agents.",
		),
		"capabilities": objectProp(
			"Live worker capabilities, e.g. {\"ipv6\": \"yes\"} (\"yes\", \"no\" or \"unknown\").",
		),
		propStatus: stringProp("\"online\" or \"offline\" for cloud regions; omitted for private ones."),
		"offlineSince": stringProp(
			"RFC3339 timestamp of when the region went dark — the last time its workers were seen.",
		),
	}
}

func listRegionsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_regions",
		Description: "List monitoring regions available to the organization (e.g. eu-west-1, " +
			"us-east-1). Returns {data: [...]} — each region's slug, label and per-region " +
			"metadata — plus defaultRegions, the org's default placement set. Use these " +
			"slugs in the regions array of create_check or update_check. Read-only: works " +
			"with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{}, nil),
		OutputSchema: objectSchema(map[string]any{
			schemaKeyData: arrayOfObjectsProp(
				"Regions available to this organization.",
				regionOutputProps(),
			),
			"defaultRegions": arrayOfStringsProp(
				"The org's default region slugs, used when a check has no explicit regions.",
			),
		}, nil),
		Annotations: readOnlyAnnotations("List regions"),
	}
}

func (h *Handler) toolListRegions(ctx context.Context, orgSlug string, _ map[string]any) ToolCallResult {
	result, err := h.regionsSvc.ListOrgRegions(ctx, orgSlug)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}
