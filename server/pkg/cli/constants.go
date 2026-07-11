package cli

// Common CLI table column headers and shared flag/argument labels.
const (
	colUID     = "UID"
	colName    = "NAME"
	colStatus  = "STATUS"
	colCreated = "CREATED"
	colActor   = "ACTOR"
	colCheck   = "CHECK"
	colSlug    = "SLUG"

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
	flagAdd      = "add"
	flagRemove   = "remove"

	flagToken       = "token"
	flagFile        = "file"
	flagDescription = "description"
	cmdCreate       = "create"
	cmdUpdate       = "update"
	cmdSet          = "set"
	cmdDelete       = "delete"
	argChildParent  = "<child-slug> <parent-slug>"

	colTimestamp = "TIMESTAMP"
	colType      = "TYPE"
	colPeriod    = "PERIOD"

	usageHumanReadableName   = "Human-readable name"
	usageOptionalDescription = "Optional description"
	usagePaginationCursor    = "Pagination cursor for next page"
	usageResultsPerPage      = "Results per page (max 100)"

	keySuccess = "success"
	keyMessage = "message"
	keyFailed  = "failed"

	keyUser         = "user"
	keyOrganization = "organization"
	keyAuthMethod   = "auth_method"
	keySlug         = "slug"

	authMethodPAT = "pat"
	authMethodJWT = "jwt"
)
