package cli

// Common CLI table column headers and shared flag/argument labels.
const (
	colUID     = "UID"
	colName    = "NAME"
	colStatus  = "STATUS"
	colCreated = "CREATED"
	colActor   = "ACTOR"
	colCheck   = "CHECK"

	argUID     = "<uid>"
	argUIDSlug = "<uid|slug>"

	flagURL      = "url"
	flagSize     = "size"
	flagStatus   = "status"
	flagCheck    = "check"
	flagList     = "list"
	flagAll      = "all"
	flagGet      = "get"
	flagType     = "type"
	flagInterval = "interval"
	flagName     = "name"
	flagEvents   = "events"
	flagCursor   = "cursor"

	colTimestamp = "TIMESTAMP"
	colType      = "TYPE"

	usageHumanReadableName = "Human-readable name"
	usagePaginationCursor  = "Pagination cursor for next page"
	usageResultsPerPage    = "Results per page (max 100)"

	keySuccess = "success"
	keyMessage = "message"
	keyFailed  = "failed"
)
