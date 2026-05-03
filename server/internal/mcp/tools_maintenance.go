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

func listMaintenanceWindowsDef() ToolDefinition {
	return ToolDefinition{
		Name:        "list_maintenance_windows",
		Description: "List maintenance windows for the organization, optionally filtered by status.",
		InputSchema: objectSchema(map[string]any{
			propStatus: stringProp(
				"Filter by lifecycle: \"upcoming\", \"active\", or \"past\". Omit for all windows.",
			),
			propLimit: intProp("Max results (1-200, default 50)."),
		}, nil),
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
	return marshalResult(windows)
}

func getMaintenanceWindowDef() ToolDefinition {
	return ToolDefinition{
		Name: "get_maintenance_window",
		Description: "Get a single maintenance window by UID, including title, schedule, " +
			"and recurrence rule.",
		InputSchema: objectSchema(map[string]any{
			propUID: stringProp("Maintenance window UID returned by list_maintenance_windows."),
		}, []string{propUID}),
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
		Description: "Schedule a new maintenance window. Optionally attach checks in the same call " +
			"by passing checkUids — the underlying service does this in two steps but the tool " +
			"handles it for you.",
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
			propRecurrence: stringProp(
				"iCalendar RRULE string for repeating windows. " +
					"Examples: \"FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR\" for weekdays, " +
					"\"FREQ=MONTHLY;BYMONTHDAY=1\" for the 1st of each month. Omit for one-off windows.",
			),
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
		Name:        "update_maintenance_window",
		Description: "Update a maintenance window (PATCH semantics — only provided fields change).",
		InputSchema: objectSchema(map[string]any{
			propUID:              stringProp("Maintenance window UID (required)."),
			propTitle:            stringProp("New title for the maintenance window."),
			propStartAt:          stringProp("New start (RFC3339, e.g. \"2026-05-03T22:00:00Z\")."),
			propEndAt:            stringProp("New end (RFC3339, must be later than startAt)."),
			schemaKeyDescription: stringProp("New free-text description shown in the UI."),
			propRecurrence: stringProp(
				"New iCalendar RRULE (e.g. \"FREQ=WEEKLY;BYDAY=MO\"). Pass empty string to clear.",
			),
			propRecurrenceEnd: stringProp("New RFC3339 recurrence end timestamp."),
		}, []string{propUID}),
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
		Name:        "delete_maintenance_window",
		Description: "Delete a maintenance window by UID (soft delete).",
		InputSchema: objectSchema(map[string]any{
			propUID: stringProp("Maintenance window UID."),
		}, []string{propUID}),
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
	return textResult("Maintenance window deleted successfully.")
}

func setMaintenanceWindowChecksDef() ToolDefinition {
	return ToolDefinition{
		Name: toolSetMaintenanceWindowCheck,
		Description: "Replace the set of checks (and/or check groups) attached to a maintenance window. " +
			"Pass empty arrays to clear. To leave one of the two collections untouched, pass it with its " +
			"current contents — partial updates are not supported by this endpoint.",
		InputSchema: objectSchema(map[string]any{
			propUID: stringProp("Maintenance window UID."),
			propCheckUIDs: arrayOfStringsProp(
				"Array of check UIDs to attach. Example: [\"uid1\",\"uid2\"]. Empty array clears.",
			),
			propCheckGroupUIDs: arrayOfStringsProp(
				"Array of check-group UIDs to attach. Example: [\"groupUid1\"]. Empty array clears.",
			),
		}, []string{propUID}),
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
	return textResult("Maintenance window checks updated successfully.")
}
