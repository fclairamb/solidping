package mcp

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/maintenancewindows"
)

const (
	propTitle          = "title"
	propStartAt        = "startAt"
	propEndAt          = "endAt"
	propRecurrence     = "recurrence"
	propRecurrenceEnd  = "recurrenceEnd"
	propStatus         = "status"
	propCheckUIDs      = "checkUids"
	propCheckGroupUIDs = "checkGroupUids"
	mwListDefaultLimit = 50
	mwListMaxLimit     = 200
)

// recurrenceDoc is the canonical description of the recurrence enum, anchored to
// startAt. iCalendar RRULE strings are NOT supported by the service.
const recurrenceDoc = "One of \"none\", \"daily\", \"weekly\", or \"monthly\". The cadence is anchored " +
	"to startAt: \"daily\" repeats startAt's time-of-day every day; \"weekly\" repeats on startAt's " +
	"weekday; \"monthly\" repeats on startAt's day-of-month (clamped to the last day of shorter " +
	"months). Each occurrence lasts endAt - startAt. Omit (or \"none\") for a one-off window. " +
	"iCalendar RRULE strings are NOT supported."

// maintenanceWindowOutputProps documents the window fields agents rely on,
// verified against maintenancewindows.MaintenanceWindowResponse's JSON tags.
// Optional (omitempty) fields are left out of `required`.
func maintenanceWindowOutputProps() map[string]any {
	return map[string]any{
		propUID:              stringProp("Maintenance window UID."),
		propTitle:            stringProp("Human-readable title."),
		schemaKeyDescription: stringProp("Free-text description, when set."),
		propStartAt:          stringProp("RFC3339 start of one occurrence."),
		propEndAt:            stringProp("RFC3339 end of one occurrence."),
		"recurrence":         stringProp("\"none\", \"daily\", \"weekly\" or \"monthly\"."),
		"recurrenceEnd":      stringProp("RFC3339 timestamp at which a recurring window stops, when set."),
		schemaKeyCreatedAt:   stringProp("RFC3339 creation timestamp."),
		schemaKeyUpdatedAt:   stringProp("RFC3339 last-update timestamp."),
		propStatus:           stringProp("Server-computed lifecycle: \"active\", \"upcoming\" or \"past\"."),
		"nextOccurrences": map[string]any{
			schemaKeyType: []string{schemaTypeArray, schemaTypeNull},
			schemaKeyItems: map[string]any{
				schemaKeyType: schemaTypeObject,
				schemaKeyProperties: map[string]any{
					propStartAt: stringProp("RFC3339 start of this occurrence."),
					propEndAt:   stringProp("RFC3339 end of this occurrence."),
				},
			},
			schemaKeyDescription: "Next concrete activations (up to 3), active one first; " +
				"null once none remain (e.g. a past one-off window).",
		},
	}
}

// maintenanceWindowOutputSchema is the output shape of the single-window
// tools (get, create, update): the window object itself.
func maintenanceWindowOutputSchema() map[string]any {
	return objectSchema(maintenanceWindowOutputProps(), []string{
		propUID, propTitle, propStartAt, propEndAt, "recurrence", propStatus,
	})
}

func listMaintenanceWindowsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_maintenance_windows",
		Description: "List maintenance windows for the organization, optionally filtered by " +
			"lifecycle status. Returns {data: [...]}; each window carries its schedule, " +
			"recurrence, server-computed status (active/upcoming/past) and nextOccurrences. " +
			"Use get_maintenance_window to inspect a single window by UID. " +
			"Read-only: works with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			propStatus: stringProp(
				"Filter by lifecycle: \"upcoming\", \"active\", or \"past\". Omit for all windows.",
			),
			propLimit: intProp("Max results (1-200, default 50)."),
		}, nil),
		OutputSchema: dataOutputSchema(
			"Maintenance windows on this page.",
			maintenanceWindowOutputProps(),
		),
		Annotations: readOnlyAnnotations("List maintenance windows"),
	}
}

func (h *Handler) toolListMaintenanceWindows(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	limit := getIntArg(args, propLimit, mwListDefaultLimit)
	if limit < 1 {
		limit = 1
	}
	if limit > mwListMaxLimit {
		limit = mwListMaxLimit
	}
	status := getStringArg(args, propStatus)
	windows, err := h.maintenanceSvc.ListMaintenanceWindows(ctx, orgSlug, status, limit)
	if err != nil {
		return errorResult(err.Error())
	}
	// Bare slice wrapped in the repo-standard {data: [...]} envelope: MCP
	// structuredContent must be an object.
	return marshalResult(map[string]any{schemaKeyData: windows})
}

func getMaintenanceWindowDef() ToolDefinition {
	return ToolDefinition{
		Name: "get_maintenance_window",
		Description: "Get one maintenance window by UID: title, schedule, recurrence, " +
			"server-computed status (active/upcoming/past) and nextOccurrences. Attached " +
			"checks are NOT part of the response — set_maintenance_window_checks fully " +
			"replaces them. Use list_maintenance_windows to search or filter. " +
			"Read-only: works with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			propUID: stringProp("Maintenance window UID returned by list_maintenance_windows."),
		}, []string{propUID}),
		OutputSchema: maintenanceWindowOutputSchema(),
		Annotations:  readOnlyAnnotations("Get maintenance window"),
	}
}

func (h *Handler) toolGetMaintenanceWindow(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	uid := getStringArg(args, propUID)
	if uid == "" {
		return errorResult("uid is required")
	}
	window, err := h.maintenanceSvc.GetMaintenanceWindow(ctx, orgSlug, uid)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(window)
}

func createMaintenanceWindowDef() ToolDefinition {
	return ToolDefinition{
		Name: "create_maintenance_window",
		Description: "Schedule a new maintenance window and return it (uid, schedule, " +
			"recurrence, status, nextOccurrences). Pass checkUids/checkGroupUids to attach " +
			"what the window suppresses in the same call; the window is created first, then " +
			"the checks are attached. Use set_maintenance_window_checks to attach checks to " +
			"an existing window and update_maintenance_window to change one. Requires the " +
			"mcp scope (mcp:read tokens are refused) and at least the user role in the " +
			"organization.",
		InputSchema: objectSchema(map[string]any{
			propTitle: stringProp("Human-readable title (required), e.g. \"DB upgrade\"."),
			propStartAt: stringProp(
				"RFC3339 start timestamp (required), e.g. \"2026-05-03T22:00:00Z\". " +
					"Must be earlier than endAt.",
			),
			propEndAt: stringProp(
				"RFC3339 end timestamp (required), e.g. \"2026-05-03T23:00:00Z\". " +
					"Must be later than startAt.",
			),
			schemaKeyDescription: stringProp("Optional free-text description of the work."),
			propRecurrence:       stringProp(recurrenceDoc),
			propRecurrenceEnd: stringProp(
				"RFC3339 timestamp at which a recurring window stops repeating. " +
					"Only meaningful when recurrence is set.",
			),
			propCheckUIDs: arrayOfStringsProp(
				"Optional list of check UIDs to apply maintenance to in one shot. " +
					"Example: [\"uid1\",\"uid2\"]. Pass an empty array (or omit) for no checks.",
			),
			propCheckGroupUIDs: arrayOfStringsProp(
				"Optional list of check-group UIDs to apply maintenance to. " +
					"Example: [\"groupUid1\"]. Pass an empty array (or omit) for no groups.",
			),
		}, []string{propTitle, propStartAt, propEndAt}),
		OutputSchema: maintenanceWindowOutputSchema(),
		Annotations:  createAnnotations("Create maintenance window"),
	}
}

func (h *Handler) toolCreateMaintenanceWindow(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	req, errMsg := buildCreateMaintenanceRequest(args)
	if errMsg != "" {
		return errorResult(errMsg)
	}
	window, err := h.maintenanceSvc.CreateMaintenanceWindow(ctx, orgSlug, req)
	if err != nil {
		return errorResult(err.Error())
	}

	checkUIDs := getStringSliceArg(args, propCheckUIDs)
	checkGroupUIDs := getStringSliceArg(args, propCheckGroupUIDs)
	if len(checkUIDs) > 0 || len(checkGroupUIDs) > 0 {
		err := h.maintenanceSvc.SetChecks(ctx, orgSlug, window.UID, maintenancewindows.SetChecksRequest{
			CheckUIDs:      checkUIDs,
			CheckGroupUIDs: checkGroupUIDs,
		})
		if err != nil {
			return errorResult("window created but failed to attach checks: " + err.Error())
		}
	}

	return marshalResult(window)
}

func buildCreateMaintenanceRequest(args map[string]any) (*maintenancewindows.CreateRequest, string) {
	title := getStringArg(args, propTitle)
	if title == "" {
		return nil, "title is required"
	}
	startStr := getStringArg(args, propStartAt)
	endStr := getStringArg(args, propEndAt)
	if startStr == "" || endStr == "" {
		return nil, "startAt and endAt are required (RFC3339)"
	}
	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		return nil, "startAt must be RFC3339: " + err.Error()
	}
	end, err := time.Parse(time.RFC3339, endStr)
	if err != nil {
		return nil, "endAt must be RFC3339: " + err.Error()
	}

	req := &maintenancewindows.CreateRequest{
		Title:      title,
		StartAt:    start,
		EndAt:      end,
		Recurrence: getStringArg(args, propRecurrence),
	}
	if v := getStringArg(args, schemaKeyDescription); v != "" {
		req.Description = &v
	}
	if v := getStringArg(args, propRecurrenceEnd); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return nil, "recurrenceEnd must be RFC3339: " + perr.Error()
		}
		req.RecurrenceEnd = &t
	}
	return req, ""
}

func updateMaintenanceWindowDef() ToolDefinition {
	return ToolDefinition{
		Name: "update_maintenance_window",
		Description: "Update an existing maintenance window by UID and return the updated " +
			"window. PATCH semantics — only the fields you pass change, and the UID must " +
			"refer to a live window (soft-deleted or unknown UIDs are not-found). Moving " +
			"startAt re-anchors any recurrence to the new start, and the effective end must " +
			"stay after start. Use create_maintenance_window to schedule a new window and " +
			"set_maintenance_window_checks to change which checks it covers — this tool " +
			"never touches attachments. Requires the mcp scope (mcp:read tokens are " +
			"refused) and at least the user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propUID:              stringProp("Maintenance window UID (required)."),
			propTitle:            stringProp("New title for the maintenance window."),
			propStartAt:          stringProp("New start (RFC3339, e.g. \"2026-05-03T22:00:00Z\")."),
			propEndAt:            stringProp("New end (RFC3339, must be later than startAt)."),
			schemaKeyDescription: stringProp("New free-text description shown in the UI."),
			propRecurrence: stringProp(recurrenceDoc +
				" When updating, omitting this field keeps the current recurrence; " +
				"pass \"none\" to clear it (make the window one-off)."),
			propRecurrenceEnd: stringProp("New RFC3339 recurrence end timestamp."),
		}, []string{propUID}),
		OutputSchema: maintenanceWindowOutputSchema(),
		Annotations:  updateAnnotations("Update maintenance window"),
	}
}

func (h *Handler) toolUpdateMaintenanceWindow(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	uid := getStringArg(args, propUID)
	if uid == "" {
		return errorResult("uid is required")
	}
	req, errMsg := buildUpdateMaintenanceRequest(args)
	if errMsg != "" {
		return errorResult(errMsg)
	}
	window, err := h.maintenanceSvc.UpdateMaintenanceWindow(ctx, orgSlug, uid, *req)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(window)
}

func buildUpdateMaintenanceRequest(args map[string]any) (*maintenancewindows.UpdateRequest, string) {
	req := &maintenancewindows.UpdateRequest{}
	if v := getStringArg(args, propTitle); v != "" {
		req.Title = &v
	}
	if v := getStringArg(args, schemaKeyDescription); v != "" {
		req.Description = &v
	}
	if v := getStringArg(args, propStartAt); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, "startAt must be RFC3339: " + err.Error()
		}
		req.StartAt = &t
	}
	if v := getStringArg(args, propEndAt); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, "endAt must be RFC3339: " + err.Error()
		}
		req.EndAt = &t
	}
	if _, ok := args[propRecurrence]; ok {
		v := getStringArg(args, propRecurrence)
		req.Recurrence = &v
	}
	if v := getStringArg(args, propRecurrenceEnd); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, "recurrenceEnd must be RFC3339: " + err.Error()
		}
		req.RecurrenceEnd = &t
	}
	return req, ""
}

func deleteMaintenanceWindowDef() ToolDefinition {
	return ToolDefinition{
		Name: "delete_maintenance_window",
		Description: "Delete a maintenance window by UID. Soft delete: it vanishes from " +
			"list_maintenance_windows and get_maintenance_window immediately and stops " +
			"suppressing its checks; the row survives in the database, but no endpoint " +
			"restores it, so treat deletion as permanent. Returns {deleted: true, uid}. " +
			"Use update_maintenance_window to keep the window with changes, or " +
			"set_maintenance_window_checks to change only what it covers. Requires the mcp " +
			"scope (mcp:read tokens are refused) and at least the user role in the " +
			"organization.",
		InputSchema: objectSchema(map[string]any{
			propUID: stringProp("Maintenance window UID."),
		}, []string{propUID}),
		OutputSchema: objectSchema(map[string]any{
			schemaKeyDeleted: boolProp("Always true on success."),
			propUID:          stringProp("UID of the deleted maintenance window."),
		}, []string{schemaKeyDeleted, propUID}),
		Annotations: deleteAnnotations("Delete maintenance window"),
	}
}

func (h *Handler) toolDeleteMaintenanceWindow(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	uid := getStringArg(args, propUID)
	if uid == "" {
		return errorResult("uid is required")
	}
	if err := h.maintenanceSvc.DeleteMaintenanceWindow(ctx, orgSlug, uid); err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(map[string]any{schemaKeyDeleted: true, propUID: uid})
}

func setMaintenanceWindowChecksDef() ToolDefinition {
	return ToolDefinition{
		Name: toolSetMaintenanceWindowCheck,
		Description: "Replace the set of checks and check groups attached to an existing " +
			"maintenance window, in one write. Both lists are set together: a list you omit " +
			"or pass empty clears that side, and a list you keep must be passed with its " +
			"current contents (partial updates are not supported). Returns {updated: true, " +
			"checkUids, checkGroupUids} — the attachments as they now stand. Use it " +
			"instead of create_maintenance_window to attach checks to an existing window; " +
			"update_maintenance_window never touches attachments. Requires the mcp scope " +
			"(mcp:read tokens are refused) and at least the user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propUID: stringProp("Maintenance window UID."),
			propCheckUIDs: arrayOfStringsProp(
				"Array of check UIDs to attach. Example: [\"uid1\",\"uid2\"]. Empty array clears.",
			),
			propCheckGroupUIDs: arrayOfStringsProp(
				"Array of check-group UIDs to attach. Example: [\"groupUid1\"]. Empty array clears.",
			),
		}, []string{propUID}),
		OutputSchema: objectSchema(map[string]any{
			schemaKeyUpdated:   boolProp("Always true on success."),
			propCheckUIDs:      arrayOfStringsProp("Check UIDs attached after this call (empty if cleared)."),
			propCheckGroupUIDs: arrayOfStringsProp("Check-group UIDs attached after this call (empty if cleared)."),
		}, []string{schemaKeyUpdated, propCheckUIDs, propCheckGroupUIDs}),
		Annotations: replaceAnnotations("Set maintenance window checks"),
	}
}

func (h *Handler) toolSetMaintenanceWindowChecks(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	uid := getStringArg(args, propUID)
	if uid == "" {
		return errorResult("uid is required")
	}
	checkUIDs := getStringSliceArg(args, propCheckUIDs)
	groupUIDs := getStringSliceArg(args, propCheckGroupUIDs)
	if checkUIDs == nil {
		checkUIDs = []string{}
	}
	if groupUIDs == nil {
		groupUIDs = []string{}
	}
	err := h.maintenanceSvc.SetChecks(ctx, orgSlug, uid, maintenancewindows.SetChecksRequest{
		CheckUIDs:      checkUIDs,
		CheckGroupUIDs: groupUIDs,
	})
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(map[string]any{
		schemaKeyUpdated:   true,
		propCheckUIDs:      checkUIDs,
		propCheckGroupUIDs: groupUIDs,
	})
}
