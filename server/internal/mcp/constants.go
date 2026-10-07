package mcp

// Canonical descriptions for cross-cutting parameters. Keep these in one
// place so the same logical concept is described identically in every tool.
const (
	descIdentifier = "Check UID or URL-friendly slug, e.g. \"api-prod\" or " +
		"\"63d49e55-97e3-4e8c-b7ab-c862de7a43f3\"."
	descRFC3339Lower = "RFC3339 timestamp (inclusive lower bound), " +
		"e.g. \"2026-05-03T10:14:22Z\"."
	descRFC3339Upper = "RFC3339 timestamp (exclusive upper bound), " +
		"e.g. \"2026-05-03T11:00:00Z\"."
	descRegionsFilter = "Comma-separated region slugs, e.g. \"eu-west-1,us-east-1\"."
	descLabelFilter   = "Label filter as \"key:value,key2:value2\". " +
		"Example: \"env:prod,team:api\"."
	descLimit  = "Max results (1-100, default 20)."
	descCursor = "Opaque pagination cursor returned by a previous response. " +
		"Omit on the first page."
)

// JSON Schema and JSON-RPC constants used across MCP tool definitions.
const (
	jsonRPCVersion = "2.0"

	mimeTypeJSON = "application/json"

	contentTypeText = "text"

	methodInitialize    = "initialize"
	methodInitialized   = "notifications/initialized"
	methodPing          = "ping"
	methodToolsList     = "tools/list"
	methodToolsCall     = "tools/call"
	methodResourcesList = "resources/list"
	methodResourcesRead = "resources/read"
	methodPromptsList   = "prompts/list"
	methodPromptsGet    = "prompts/get"

	uriOrganization = "solidping://organization"
	uriRegions      = "solidping://regions"

	schemaKeyType            = "type"
	schemaKeyDescription     = "description"
	schemaKeyItems           = "items"
	schemaKeyProperties      = "properties"
	schemaKeyName            = "name"
	schemaKeySlug            = "slug"
	schemaKeyData            = "data"
	schemaKeyConfig          = "config"
	schemaKeyEnabled         = "enabled"
	schemaKeyPagination      = "pagination"
	schemaKeyWindow          = "window"
	schemaKeyEffectiveFilter = "effectiveFilter"
	schemaKeyPlacement       = "placement"
	schemaKeyPeriod          = "period"
	schemaKeyDeleted         = "deleted"
	schemaKeyCreatedAt       = "createdAt"
	schemaKeyUpdatedAt       = "updatedAt"
	schemaKeyStartedAt       = "startedAt"
	schemaKeyResolvedAt      = "resolvedAt"
	schemaKeyRegion          = "region"
	schemaKeyRegions         = "regions"
	schemaKeyUpdated         = "updated"
	schemaKeyUnpublished     = "unpublished"
	schemaKeyCheck           = "check"

	// JSON Schema type VALUES (the schemaKey* constants above are KEYS).
	schemaTypeObject = "object"
	schemaTypeArray  = "array"
	schemaTypeNumber = "number"
	schemaTypeNull   = "null"

	propLabels           = "labels"
	propCheckGroupUID    = "checkGroupUid"
	propWith             = "with"
	propLimit            = "limit"
	propCursor           = "cursor"
	propIdentifier       = "identifier"
	propUID              = "uid"
	propPeriodStartAfter = "periodStartAfter"
	propPeriodEndBefore  = "periodEndBefore"
)

// Canonical tool names. Prefer constants over string literals so that
// scope-gating tests, dispatch wiring, and tool definitions stay in sync.
const (
	toolListChecks                = "list_checks"
	toolGetCheck                  = "get_check"
	toolCreateCheck               = "create_check"
	toolUpdateCheck               = "update_check"
	toolDeleteCheck               = "delete_check"
	toolDiagnoseCheck             = "diagnose_check"
	toolRunCheck                  = "run_check"
	toolValidateCheck             = "validate_check"
	toolRunJSScript               = "run_js_script"
	toolFetchPage                 = "fetch_page"
	toolBrowserSnapshot           = "browser_snapshot"
	toolSetMaintenanceWindowCheck = "set_maintenance_window_checks"
)
