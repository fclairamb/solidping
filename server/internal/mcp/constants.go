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

	uriOrganization = "solidping://organization"
	uriRegions      = "solidping://regions"

	schemaKeyType        = "type"
	schemaKeyDescription = "description"
	schemaKeyItems       = "items"
	schemaKeyName        = "name"
	schemaKeySlug        = "slug"
	schemaKeyData        = "data"
	schemaKeyConfig      = "config"
	schemaKeyEnabled     = "enabled"

	propLabels        = "labels"
	propCheckGroupUID = "checkGroupUid"
	propWith          = "with"
	propLimit         = "limit"
	propCursor        = "cursor"
	propIdentifier    = "identifier"
	propUID           = "uid"
)
