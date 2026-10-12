package mcp

// The mutation gate (spec 2026-10-12-06). Scope, role and demo all ask one
// question before a tools/call runs: does this call write? The answer comes
// from toolAccessTable, an explicit per-tool entry, and no longer from the
// tool's name prefix. A tool that merges several actions under one name
// (manage_status_page_section and friends) cannot be classified by its name,
// so it is classified by its `action` argument instead.
//
// The table fails closed on every axis: a tool with no entry, a by-action tool
// called without an action, with a non-string action or with an action that is
// not one of its declared READ actions, all count as writes.

// toolAccessKind is how a tool's write-ness is decided.
type toolAccessKind int

const (
	// accessRead: the tool never writes.
	accessRead toolAccessKind = iota + 1
	// accessWrite: every call writes.
	accessWrite
	// accessByAction: the `action` argument decides; only the actions listed
	// in readActions are reads, anything else (missing, malformed, unknown,
	// differently cased) is a write.
	accessByAction
)

// propAction is the argument a by-action tool is dispatched on.
const propAction = "action"

type toolAccess struct {
	kind        toolAccessKind
	readActions map[string]struct{}
}

func readTool() toolAccess  { return toolAccess{kind: accessRead} }
func writeTool() toolAccess { return toolAccess{kind: accessWrite} }

// byActionTool declares a by-action tool. With no read actions, every call is
// a write, which is what every manage_* tool is today: the scope gate refuses
// all of them to a read-only token.
func byActionTool(readActions ...string) toolAccess {
	set := make(map[string]struct{}, len(readActions))
	for _, action := range readActions {
		set[action] = struct{}{}
	}

	return toolAccess{kind: accessByAction, readActions: set}
}

// toolAccessTable classifies every callable tool name, listed or alias.
// TestEveryToolHasAnAccessEntry fails when a registered tool is missing here.
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const maps.
var toolAccessTable = map[string]toolAccess{
	// Checks
	toolListChecks:    readTool(),
	toolGetCheck:      readTool(),
	toolCreateCheck:   writeTool(),
	toolUpdateCheck:   writeTool(),
	toolDeleteCheck:   writeTool(),
	toolValidateCheck: readTool(),
	// run_check queues a run: it changes what the scheduler does next.
	toolRunCheck:      writeTool(),
	toolDiagnoseCheck: readTool(),

	// Results, regions, groups, check types
	toolListResults:         readTool(),
	toolListRegions:         readTool(),
	toolListCheckGroups:     readTool(),
	toolListCheckTypes:      readTool(),
	toolGetCheckTypeSamples: readTool(),

	// Incidents
	toolListIncidents:             readTool(),
	toolGetIncident:               readTool(),
	toolIncidentNotificationsList: readTool(),

	// Probes: they reach the outside world but write nothing in SolidPing.
	toolRunJSScript:     readTool(),
	toolFetchPage:       readTool(),
	toolBrowserSnapshot: readTool(),

	// Integrations
	toolListIntegrations:  readTool(),
	toolCreateIntegration: writeTool(),

	// Maintenance windows
	toolListMaintenanceWindows:    readTool(),
	toolGetMaintenanceWindow:      readTool(),
	toolCreateMaintenanceWindow:   writeTool(),
	toolUpdateMaintenanceWindow:   writeTool(),
	toolDeleteMaintenanceWindow:   writeTool(),
	toolSetMaintenanceWindowCheck: writeTool(),

	// Status pages
	toolListStatusPages:  readTool(),
	toolGetStatusPage:    readTool(),
	toolCreateStatusPage: writeTool(),
	toolUpdateStatusPage: writeTool(),
	toolDeleteStatusPage: writeTool(),

	// Status page sub-resources
	toolListStatusPageSections:         readTool(),
	toolCreateStatusPageSection:        writeTool(),
	toolUpdateStatusPageSection:        writeTool(),
	toolDeleteStatusPageSection:        writeTool(),
	toolListStatusPageResources:        readTool(),
	toolCreateStatusPageResource:       writeTool(),
	toolUpdateStatusPageResource:       writeTool(),
	toolDeleteStatusPageResource:       writeTool(),
	toolListStatusPageIncidents:        readTool(),
	toolCreateStatusPageIncident:       writeTool(),
	toolUpdateStatusPageIncident:       writeTool(),
	toolCreateStatusPageIncidentUpdate: writeTool(),
	toolCreateIncidentPublication:      writeTool(),
	toolDeleteIncidentPublication:      writeTool(),
}

// toolMutates reports whether calling tool `name` with `args` writes, and so
// must pass the scope, demo and role gates. Unknown tools are writes.
func toolMutates(name string, args map[string]any) bool {
	access, known := toolAccessTable[name]
	if !known {
		return true
	}

	return accessMutates(access, args)
}

// accessMutates applies one table entry to one call's arguments.
func accessMutates(access toolAccess, args map[string]any) bool {
	switch access.kind {
	case accessRead:
		return false
	case accessByAction:
		action, isString := args[propAction].(string)
		if !isString {
			return true
		}

		_, isRead := access.readActions[action]

		return !isRead
	case accessWrite:
		return true
	default:
		return true
	}
}

// toolIsReadOnly reports whether no call of the tool can ever write: the only
// tools that may advertise readOnlyHint.
func toolIsReadOnly(name string) bool {
	access, known := toolAccessTable[name]

	return known && access.kind == accessRead
}
