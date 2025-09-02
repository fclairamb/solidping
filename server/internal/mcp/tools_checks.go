package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

func listChecksDef() ToolDefinition {
	return ToolDefinition{
		Name:        "list_checks",
		Description: "List monitoring checks for the organization.",
		InputSchema: objectSchema(map[string]any{
			"q":             stringProp("Search query (name or slug substring)"),
			"labels":        stringProp("Label filter (key:value,key2:value2)"),
			"checkGroupUid": stringProp("Filter by check group UID or slug"),
			"with":          stringProp("Include extra fields: lastResult, lastStatusChange (comma-separated)"),
			"limit":         intProp("Max results (1-100, default 20)"),
			"cursor":        stringProp("Pagination cursor from previous response"),
		}, nil),
	}
}

func (h *Handler) toolListChecks(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	opts := checks.ListChecksOptions{
		Query:  getStringArg(args, "q"),
		Cursor: getStringArg(args, "cursor"),
		Limit:  getIntArg(args, "limit", 20),
	}

	if opts.Limit < 1 {
		opts.Limit = 1
	}
	if opts.Limit > 100 {
		opts.Limit = 100
	}

	if labelsParam := getStringArg(args, "labels"); labelsParam != "" {
		opts.Labels = make(map[string]string)
		for _, pair := range strings.Split(labelsParam, ",") {
			kv := strings.SplitN(pair, ":", 2)
			if len(kv) == 2 {
				opts.Labels[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
			}
		}
	}

	if checkGroupUID := getStringArg(args, "checkGroupUid"); checkGroupUID != "" {
		opts.CheckGroupUID = &checkGroupUID
	}

	if withParam := getStringArg(args, "with"); withParam != "" {
		for _, part := range strings.Split(withParam, ",") {
			switch strings.TrimSpace(part) {
			case "lastResult":
				opts.IncludeLastResult = true
			case "lastStatusChange":
				opts.IncludeLastStatusChange = true
			}
		}
	}

	result, err := h.checksSvc.ListChecks(ctx, orgSlug, opts)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}

func getCheckDef() ToolDefinition {
	return ToolDefinition{
		Name:        "get_check",
		Description: "Get a single check by UID or slug.",
		InputSchema: objectSchema(map[string]any{
			"identifier": stringProp("Check UID or slug"),
			"with":       stringProp("Include extra fields: lastResult, lastStatusChange"),
		}, []string{"identifier"}),
	}
}

func (h *Handler) toolGetCheck(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	identifier := getStringArg(args, "identifier")
	if identifier == "" {
		return errorResult("identifier is required")
	}

	opts := checks.GetCheckOptions{}
	if withParam := getStringArg(args, "with"); withParam != "" {
		for _, part := range strings.Split(withParam, ",") {
			switch strings.TrimSpace(part) {
			case "lastResult":
				opts.IncludeLastResult = true
			case "lastStatusChange":
				opts.IncludeLastStatusChange = true
			}
		}
	}

	result, err := h.checksSvc.GetCheck(ctx, orgSlug, identifier, opts)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}

func createCheckDef() ToolDefinition {
	return ToolDefinition{
		Name:        "create_check",
		Description: "Create a new monitoring check.",
		InputSchema: objectSchema(map[string]any{
			"name": stringProp("Human-readable name (auto-generated from URL if omitted)"),
			"slug": stringProp("URL-friendly identifier (auto-generated if omitted)"),
			"type": stringProp(
				"Check type: http, tcp, icmp, dns, ssl, heartbeat, domain (inferred if omitted)",
			),
			"config":        objectProp("Check-specific config (e.g., {\"url\": \"https://example.com\"})"),
			"regions":       arrayOfStringsProp("Region slugs (e.g., [\"eu-west-1\", \"us-east-1\"])"),
			"enabled":       boolProp("Default true"),
			"period":        stringProp("Check interval (e.g., \"00:00:30\" for 30s, default \"00:01:00\")"),
			"labels":        objectProp("Key-value labels (e.g., {\"env\": \"production\"})"),
			"description":   stringProp("Free-text description"),
			"checkGroupUid": stringProp("Assign to a check group"),
		}, []string{"config"}),
	}
}

func (h *Handler) toolCreateCheck(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	config := getMapArg(args, "config")
	if config == nil {
		return errorResult("config is required")
	}

	req := checks.CreateCheckRequest{
		Name:        getStringArg(args, "name"),
		Slug:        getStringArg(args, "slug"),
		Type:        getStringArg(args, "type"),
		Config:      config,
		Regions:     getStringSliceArg(args, "regions"),
		Enabled:     getBoolArg(args, "enabled"),
		Description: getStringArg(args, "description"),
		Labels:      getStringMapArg(args, "labels"),
	}

	if p := getStringArg(args, "period"); p != "" {
		req.Period = &p
	}

	if g := getStringArg(args, "checkGroupUid"); g != "" {
		req.CheckGroupUID = &g
	}

	result, err := h.checksSvc.CreateCheck(ctx, orgSlug, req)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}

func updateCheckDef() ToolDefinition {
	return ToolDefinition{
		Name:        "update_check",
		Description: "Update an existing check by UID or slug. Only provided fields are modified (PATCH semantics).",
		InputSchema: objectSchema(map[string]any{
			"identifier":    stringProp("Check UID or slug"),
			"name":          stringProp("New name"),
			"slug":          stringProp("New slug"),
			"config":        objectProp("Updated config"),
			"regions":       arrayOfStringsProp("Updated regions"),
			"enabled":       boolProp("Enable/disable"),
			"period":        stringProp("New check interval"),
			"labels":        objectProp("Replace labels (empty object clears)"),
			"description":   stringProp("Updated description"),
			"checkGroupUid": stringProp("Move to different group (empty string to ungroup)"),
		}, []string{"identifier"}),
	}
}

func (h *Handler) toolUpdateCheck(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	identifier := getStringArg(args, "identifier")
	if identifier == "" {
		return errorResult("identifier is required")
	}

	req := checks.UpdateCheckRequest{}

	if v := getStringArg(args, "name"); v != "" {
		req.Name = &v
	}
	if v := getStringArg(args, "slug"); v != "" {
		req.Slug = &v
	}
	if v := getStringArg(args, "description"); v != "" {
		req.Description = &v
	}
	if v := getMapArg(args, "config"); v != nil {
		req.Config = &v
	}
	if v := getStringSliceArg(args, "regions"); v != nil {
		req.Regions = &v
	}
	req.Enabled = getBoolArg(args, "enabled")
	if v := getStringArg(args, "period"); v != "" {
		req.Period = &v
	}
	if v := getStringMapArg(args, "labels"); v != nil {
		req.Labels = &v
	}
	if _, ok := args["checkGroupUid"]; ok {
		g := getStringArg(args, "checkGroupUid")
		req.CheckGroupUID = &g
	}

	result, err := h.checksSvc.UpdateCheck(ctx, orgSlug, identifier, &req)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}

func deleteCheckDef() ToolDefinition {
	return ToolDefinition{
		Name:        "delete_check",
		Description: "Delete a check by UID or slug (soft delete).",
		InputSchema: objectSchema(map[string]any{
			"identifier": stringProp("Check UID or slug"),
		}, []string{"identifier"}),
	}
}

func (h *Handler) toolDeleteCheck(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	identifier := getStringArg(args, "identifier")
	if identifier == "" {
		return errorResult("identifier is required")
	}

	if err := h.checksSvc.DeleteCheck(ctx, orgSlug, identifier); err != nil {
		return errorResult(err.Error())
	}

	return textResult("Check deleted successfully.")
}

func marshalResult(v any) ToolCallResult {
	data, err := json.Marshal(v)
	if err != nil {
		return errorResult("Failed to marshal result: " + err.Error())
	}
	return textResult(string(data))
}
