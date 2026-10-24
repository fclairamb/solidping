package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Merged tools (spec 2026-10-12-06). Each one replaces several older tools
// with a `kind`, `action` or `include` argument and calls exactly the code the
// older tools ran; the older names stay callable as legacy aliases (see
// registerTools), so there is one implementation per behavior.

const (
	toolProbe                    = "probe"
	toolManageStatusPageSection  = "manage_status_page_section"
	toolManageStatusPageResource = "manage_status_page_resource"
	toolManageStatusPageIncident = "manage_status_page_incident"

	propInclude = "include"

	probeKindScript  = "script"
	probeKindFetch   = "fetch"
	probeKindBrowser = "browser"

	actionCreate    = "create"
	actionUpdate    = "update"
	actionDelete    = "delete"
	actionAddUpdate = "add_update"
	actionPublish   = "publish"
	actionUnpublish = "unpublish"

	includeSections      = "sections"
	includeResources     = "resources"
	includeIncidents     = "incidents"
	includeNotifications = "notifications"
)

var (
	errIncludeNotStrings = errors.New("include must be an array of strings")
	errUnknownInclude    = errors.New("unknown include")
)

// enumStringProp is a string property restricted to the given values.
func enumStringProp(desc string, values ...string) map[string]any {
	return map[string]any{
		schemaKeyType:        schemaTypeString,
		schemaKeyDescription: desc,
		"enum":               values,
	}
}

// includeProp is the `include` argument of a read merge.
func includeProp(desc string, values ...string) map[string]any {
	return map[string]any{
		schemaKeyType:        schemaTypeArray,
		schemaKeyItems:       map[string]any{schemaKeyType: schemaTypeString, "enum": values},
		schemaKeyDescription: desc,
	}
}

// schemaProps returns a copy of an object schema's properties.
func schemaProps(schema any) map[string]any {
	object, _ := schema.(map[string]any)
	props, _ := object[schemaKeyProperties].(map[string]any)

	return maps.Clone(props)
}

// mergedProps is the union of the input (or output) properties of the tools a
// merge replaces. The first definition of a property wins, so each argument
// keeps the one description its original tool gave it.
func mergedProps(schemas ...any) map[string]any {
	merged := map[string]any{}

	for _, schema := range schemas {
		for key, value := range schemaProps(schema) {
			if _, exists := merged[key]; !exists {
				merged[key] = value
			}
		}
	}

	return merged
}

// withFixedArg returns fn called with args[key] forced to value: how a legacy
// alias reaches the merged tool that replaced it.
func withFixedArg(toolFn toolFunc, key, value string) toolFunc {
	return func(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
		fixed := maps.Clone(args)
		if fixed == nil {
			fixed = map[string]any{}
		}

		fixed[key] = value

		return toolFn(ctx, orgSlug, fixed)
	}
}

// namedFunc is one value of a dispatch argument (a probe kind, a manage action).
type namedFunc struct {
	name string
	fn   toolFunc
}

// dispatchOn calls the function named by args[key]. A missing, non-string or
// unknown value (matched exactly, case included) is a tool error naming the
// valid values; nothing runs.
func dispatchOn(
	ctx context.Context, orgSlug string, args map[string]any, key string, choices []namedFunc,
) ToolCallResult {
	names := make([]string, len(choices))
	for i := range choices {
		names[i] = choices[i].name
	}

	value, isString := args[key].(string)
	if !isString || value == "" {
		return errorResult(fmt.Sprintf("%s is required: one of %s", key, strings.Join(names, ", ")))
	}

	for i := range choices {
		if choices[i].name == value {
			return choices[i].fn(ctx, orgSlug, args)
		}
	}

	return errorResult(fmt.Sprintf("unknown %s %q: one of %s", key, value, strings.Join(names, ", ")))
}

// parseInclude reads an `include` argument against its allowed values. An
// absent include is empty; an unknown value is an error, never ignored.
func parseInclude(args map[string]any, allowed ...string) (map[string]bool, error) {
	raw, present := args[propInclude]
	if !present || raw == nil {
		return map[string]bool{}, nil
	}

	var items []string

	switch value := raw.(type) {
	case string:
		items = strings.Split(value, ",")
	case []string:
		items = value
	case []any:
		for _, item := range value {
			str, ok := item.(string)
			if !ok {
				return nil, errIncludeNotStrings
			}

			items = append(items, str)
		}
	default:
		return nil, errIncludeNotStrings
	}

	include := map[string]bool{}

	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		if !slices.Contains(allowed, item) {
			return nil, fmt.Errorf("%w %q: allowed values are %s", errUnknownInclude, item, strings.Join(allowed, ", "))
		}

		include[item] = true
	}

	return include, nil
}

// resultData extracts the `data` list of a list tool's successful result.
func resultData(res ToolCallResult) any {
	if object, ok := res.StructuredContent.(map[string]any); ok {
		return object[schemaKeyData]
	}

	return nil
}

// toObject turns a DTO into a map, so a read merge can add included lists
// next to its fields.
func toObject(value any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}

	return object, nil
}

// --- probe ---

func probeDef() ToolDefinition {
	props := mergedProps(runJSScriptDef().InputSchema, fetchPageDef().InputSchema, browserSnapshotDef().InputSchema)
	props[propKind] = enumStringProp(
		"What to run. \"script\": run a js check script once (needs script). \"fetch\": HTTP GET url "+
			"and return status, headers and the first 16 KB of the body. \"browser\": open url in the "+
			"headless browser and return its accessibility-style tree with a CSS selector per element.",
		probeKindScript, probeKindFetch, probeKindBrowser)

	return ToolDefinition{
		Name: toolProbe,
		Description: "Explore a target and test a `js` check before create_check, without saving " +
			"anything. kind=script runs a js check script exactly as a SolidPing worker would and " +
			"returns status, output (with console) and metrics; `env` carries plaintext parameters " +
			"and `secrets` only NAMES the secret parameters, which run empty. kind=fetch GETs a URL " +
			"from the server. kind=browser snapshots a page to pick selectors for browser steps. " +
			"kind selects the mode; the other params apply only to their kind (script: " +
			"script/env/secrets/timeout; fetch and browser: url). Reaches the URL or script " +
			"target you name. Read-only: works with mcp:read tokens.",
		InputSchema:  objectSchema(props, []string{propKind}),
		OutputSchema: runResultOutputSchema(),
		Annotations:  probeAnnotations("Probe a target"),
	}
}

func (h *Handler) toolProbe(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	return dispatchOn(ctx, orgSlug, args, propKind, []namedFunc{
		{probeKindScript, h.toolRunJSScript},
		{probeKindFetch, h.toolFetchPage},
		{probeKindBrowser, h.toolBrowserSnapshot},
	})
}

// --- manage_status_page_* ---

// manageDescriptionSuffix closes every manage_* description: the tool is
// annotated for its worst action.
const manageDescriptionSuffix = " Annotated destructive and non-idempotent because delete is one of " +
	"its actions. Every action requires the mcp scope (mcp:read tokens are refused) and at least " +
	"the user role in the organization."

func manageStatusPageSectionDef() ToolDefinition {
	props := mergedProps(createStatusPageSectionDef().InputSchema, updateStatusPageSectionDef().InputSchema)
	props[propAction] = enumStringProp("What to do with the section.", actionCreate, actionUpdate, actionDelete)

	return ToolDefinition{
		Name: toolManageStatusPageSection,
		Description: "Create, update or delete a section of a status page. action=create adds a " +
			"section (name and slug required) and returns it. action=update edits one " +
			"(sectionIdentifier required; omitted fields are kept) and returns it. action=delete " +
			"permanently removes a section and every resource pinned in it, returning {deleted, " +
			"identifier}. Read a page's sections with get_status_page include=sections." +
			manageDescriptionSuffix,
		InputSchema: objectSchema(props, []string{propAction, propPageIdentifier}),
		OutputSchema: objectSchema(mergedProps(
			createStatusPageSectionDef().OutputSchema, deleteStatusPageSectionDef().OutputSchema), nil),
		Annotations: manageAnnotations("Manage status page section"),
	}
}

func (h *Handler) toolManageStatusPageSection(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	return dispatchOn(ctx, orgSlug, args, propAction, []namedFunc{
		{actionCreate, h.toolCreateStatusPageSection},
		{actionUpdate, h.toolUpdateStatusPageSection},
		{actionDelete, h.toolDeleteStatusPageSection},
	})
}

func manageStatusPageResourceDef() ToolDefinition {
	props := mergedProps(createStatusPageResourceDef().InputSchema, updateStatusPageResourceDef().InputSchema)
	props[propAction] = enumStringProp("What to do with the resource.", actionCreate, actionUpdate, actionDelete)

	return ToolDefinition{
		Name: toolManageStatusPageResource,
		Description: "Pin, edit or unpin a resource (a check or a check group) in a status page " +
			"section. action=create pins exactly one of checkUid or checkGroupUid and returns the " +
			"resource. action=update edits one (resourceUid required; omitted fields are kept). " +
			"action=delete unpins it, returning {deleted, identifier}; the check or group itself is " +
			"untouched. Read a section's resources with get_status_page include=resources." +
			manageDescriptionSuffix,
		InputSchema: objectSchema(props, []string{propAction, propPageIdentifier, propSectionIdentifier}),
		OutputSchema: objectSchema(mergedProps(
			createStatusPageResourceDef().OutputSchema, deleteStatusPageResourceDef().OutputSchema), nil),
		Annotations: manageAnnotations("Manage status page resource"),
	}
}

func (h *Handler) toolManageStatusPageResource(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	return dispatchOn(ctx, orgSlug, args, propAction, []namedFunc{
		{actionCreate, h.toolCreateStatusPageResource},
		{actionUpdate, h.toolUpdateStatusPageResource},
		{actionDelete, h.toolDeleteStatusPageResource},
	})
}

func manageStatusPageIncidentDef() ToolDefinition {
	props := mergedProps(
		createStatusPageIncidentDef().InputSchema,
		updateStatusPageIncidentDef().InputSchema,
		createStatusPageIncidentUpdateDef().InputSchema,
		createIncidentPublicationDef().InputSchema,
		deleteIncidentPublicationDef().InputSchema,
	)
	props[propAction] = enumStringProp("What to do with the customer-facing incident.",
		actionCreate, actionUpdate, actionAddUpdate, actionPublish, actionUnpublish)

	return ToolDefinition{
		Name: toolManageStatusPageIncident,
		Description: "Manage the customer-facing incidents a status page shows (publications), " +
			"distinct from the internal incidents list_incidents returns. Titles and bodies are " +
			"shown to CUSTOMERS: never paste probe output, error strings, internal hostnames or IPs. " +
			"action=create publishes a hand-written incident (pageIdentifier, title). " +
			"action=update edits a publication's title, severity or state (pageIdentifier, " +
			"publicationUid; PATCH semantics). action=add_update appends a narrative entry " +
			"(pageIdentifier, publicationUid, kind, bodyMarkdown); entries are append-only. " +
			"action=publish relays an EXISTING internal incident (incidentUid, pageIdentifier) with a " +
			"title templated from the page. action=unpublish removes a relayed incident from the page " +
			"(incidentUid, publicationUid), keeping the row for audit. create, add_update and publish " +
			"notify status-page subscribers (hourly cap) and every action reaches webhook connections. " +
			"Read publications with get_status_page include=incidents." + manageDescriptionSuffix,
		InputSchema: objectSchema(props, []string{propAction}),
		OutputSchema: objectSchema(mergedProps(
			incidentPublicationOutputSchema(),
			publicationUpdateOutputSchema(),
			deleteIncidentPublicationDef().OutputSchema,
		), nil),
		Annotations: manageAnnotations("Manage status page incident"),
	}
}

func (h *Handler) toolManageStatusPageIncident(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	return dispatchOn(ctx, orgSlug, args, propAction, []namedFunc{
		{actionCreate, h.toolCreateStatusPageIncident},
		{actionUpdate, h.toolUpdateStatusPageIncident},
		{actionAddUpdate, h.toolCreateStatusPageIncidentUpdate},
		{actionPublish, h.toolCreateIncidentPublication},
		{actionUnpublish, h.toolDeleteIncidentPublication},
	})
}

// legacyDef marks a replaced tool's definition as a deprecated alias.
func legacyDef(build func() ToolDefinition, replacement string) ToolDefinition {
	def := build()
	def.Description = "Deprecated: use " + replacement + " instead; this name is kept for " +
		"existing clients and will be removed. " + def.Description

	return def
}
