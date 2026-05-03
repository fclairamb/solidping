package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

func listChecksDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_checks",
		Description: "List monitoring checks for the organization, optionally filtered by " +
			"name/slug substring, labels, or check group. Use this for browsing or " +
			"filtering a fleet of checks. To investigate a single check's current " +
			"health, use diagnose_check instead.",
		InputSchema: objectSchema(map[string]any{
			"q": stringProp(
				"Case-insensitive substring match on check name or slug, e.g. \"api\".",
			),
			propLabels: objectProp(
				"Label filter as a JSON object. Returns checks that have ALL of the " +
					"given labels with matching values (AND semantics). " +
					"Example: {\"env\": \"production\", \"team\": \"api\"}.",
			),
			propCheckGroupUID: stringProp("Filter to checks in this group (UID or slug), e.g. \"core-services\"."),
			propWith: stringProp(
				"Comma-separated extra fields:\n" +
					"  lastResult       — most recent result for each check\n" +
					"  lastStatusChange — when each check last changed status\n" +
					"Example: \"lastResult,lastStatusChange\".",
			),
			propLimit:  intProp(descLimit),
			propCursor: stringProp(descCursor),
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

	if v, ok := args[propLabels]; ok && v != nil {
		labels, errMsg := parseLabelsArg(v)
		if errMsg != "" {
			return errorResult(errMsg)
		}
		opts.Labels = labels
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

// parseLabelsArg validates and converts the typed-any labels arg into the
// service-layer map[string]string. Returns explicit error messages so the
// LLM gets actionable feedback instead of silently-skipped malformed pairs.
func parseLabelsArg(raw any) (map[string]string, string) {
	labelsMap, isMap := raw.(map[string]any)
	if !isMap {
		return nil, "labels must be a JSON object, e.g. {\"env\":\"production\"}"
	}
	out := make(map[string]string, len(labelsMap))
	for k, val := range labelsMap {
		strVal, isStr := val.(string)
		if !isStr {
			return nil, fmt.Sprintf("labels.%s must be a string", k)
		}
		out[k] = strVal
	}
	return out, ""
}

func getCheckDef() ToolDefinition {
	return ToolDefinition{
		Name: "get_check",
		Description: "Get a single check's metadata by UID or slug. For a full triage " +
			"briefing (current status + recent results + active incidents), prefer " +
			"diagnose_check instead.",
		InputSchema: objectSchema(map[string]any{
			propIdentifier: stringProp(descIdentifier),
			propWith: stringProp(
				"Comma-separated extra fields:\n" +
					"  lastResult       — most recent result\n" +
					"  lastStatusChange — when status last changed\n" +
					"Example: \"lastResult,lastStatusChange\".",
			),
		}, []string{propIdentifier}),
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
		Name: "create_check",
		Description: "Create a new monitoring check. If you don't know what config shape a " +
			"given type expects, call get_check_type_samples first to fetch a working " +
			"starting config, then use validate_check to dry-run before creating.",
		InputSchema: objectSchema(map[string]any{
			schemaKeyName: stringProp(
				"Human-readable name, e.g. \"API production\". Auto-generated from URL if omitted.",
			),
			schemaKeySlug: stringProp(
				"URL-friendly slug (3-20 lowercase letters/digits/hyphens), e.g. \"api-prod\". " +
					"Auto-generated if omitted.",
			),
			schemaKeyType: stringProp(
				"Check type. Allowed: http, tcp, icmp, dns, ssl, heartbeat, domain. " +
					"Inferred from config if omitted.",
			),
			schemaKeyConfig: objectProp(
				"Check-specific config. Shape depends on type. Example for http: " +
					"{\"url\": \"https://example.com\", \"method\": \"GET\"}. " +
					"Use get_check_type_samples to discover the shape for other types.",
			),
			"regions": arrayOfStringsProp(
				"Region slugs to run the check from, e.g. [\"eu-west-1\",\"us-east-1\"]. " +
					"Defaults to all org regions when omitted.",
			),
			schemaKeyEnabled: boolProp("Whether the check should run. Default true."),
			"period": stringProp(
				"Check interval as HH:MM:SS, e.g. \"00:00:30\" for 30 seconds, " +
					"\"00:01:00\" for 1 minute (default).",
			),
			propLabels: objectProp(
				"Key-value labels for organization and filtering, " +
					"e.g. {\"env\":\"production\",\"team\":\"api\"}.",
			),
			schemaKeyDescription: stringProp("Free-text description shown in the UI."),
			propCheckGroupUID: stringProp(
				"Assign the check to a check group (UID or slug), e.g. \"core-services\".",
			),
		}, []string{schemaKeyConfig}),
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
		Name: "update_check",
		Description: "Update an existing check by UID or slug. PATCH semantics — only the " +
			"fields you pass are modified, others stay as-is.",
		InputSchema: objectSchema(map[string]any{
			propIdentifier:  stringProp(descIdentifier),
			schemaKeyName:   stringProp("New human-readable name, e.g. \"API production\"."),
			schemaKeySlug:   stringProp("New URL-friendly slug, e.g. \"api-prod\"."),
			schemaKeyConfig: objectProp("Replace check-specific config (full object — not merged)."),
			"regions": arrayOfStringsProp(
				"Replace region list, e.g. [\"eu-west-1\",\"us-east-1\"]. " +
					"Pass an empty array to run from no regions (effectively pauses execution).",
			),
			schemaKeyEnabled: boolProp("Toggle whether the check runs."),
			"period": stringProp(
				"New check interval as HH:MM:SS, e.g. \"00:00:30\" for 30 seconds.",
			),
			propLabels: objectProp(
				"Replace labels object (empty object clears all). " +
					"Example: {\"env\":\"staging\"}.",
			),
			schemaKeyDescription: stringProp("Updated free-text description shown in the UI."),
			propCheckGroupUID: stringProp(
				"Move to a different group (UID or slug). Pass an empty string to ungroup.",
			),
		}, []string{propIdentifier}),
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
		Name: "delete_check",
		Description: "Soft-delete a monitoring check by UID or slug. The check stops running " +
			"immediately; historical results are kept.",
		InputSchema: objectSchema(map[string]any{
			propIdentifier: stringProp(descIdentifier),
		}, []string{propIdentifier}),
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
