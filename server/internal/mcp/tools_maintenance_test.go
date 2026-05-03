package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaintenanceWindowToolDefinitions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defs := []ToolDefinition{
		listMaintenanceWindowsDef(),
		getMaintenanceWindowDef(),
		createMaintenanceWindowDef(),
		updateMaintenanceWindowDef(),
		deleteMaintenanceWindowDef(),
		setMaintenanceWindowChecksDef(),
	}

	for _, def := range defs {
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			r.NotEmpty(def.Name)
			r.NotEmpty(def.Description)
			r.NotNil(def.InputSchema)
		})
	}
}

func TestMaintenanceWindowDescriptionsIncludeExamples(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	def := createMaintenanceWindowDef()
	schema, ok := def.InputSchema.(map[string]any)
	r.True(ok)
	props, ok := schema["properties"].(map[string]any)
	r.True(ok)

	// Recurrence description must show concrete RRULE examples (LLMs guess otherwise)
	rec, ok := props[propRecurrence].(map[string]any)
	r.True(ok)
	desc, ok := rec[schemaKeyDescription].(string)
	r.True(ok)
	r.Contains(desc, "FREQ=WEEKLY")
	r.Contains(desc, "FREQ=MONTHLY")

	// startAt and endAt must show concrete RFC3339 examples
	for _, key := range []string{propStartAt, propEndAt} {
		prop, ok := props[key].(map[string]any)
		r.True(ok)
		desc, ok := prop[schemaKeyDescription].(string)
		r.True(ok)
		r.Contains(desc, "2026-")
	}
}

func TestMaintenanceWindowRequiredArgs(t *testing.T) {
	t.Parallel()

	handler := newTestHandler()

	tests := []struct {
		name        string
		tool        toolFunc
		args        map[string]any
		errContains string
	}{
		{
			name:        "get_maintenance_window rejects empty uid",
			tool:        handler.toolGetMaintenanceWindow,
			args:        map[string]any{},
			errContains: "uid is required",
		},
		{
			name:        "create_maintenance_window rejects missing title",
			tool:        handler.toolCreateMaintenanceWindow,
			args:        map[string]any{"startAt": "2026-05-03T22:00:00Z", "endAt": "2026-05-03T23:00:00Z"},
			errContains: "title is required",
		},
		{
			name:        "create_maintenance_window rejects missing time bounds",
			tool:        handler.toolCreateMaintenanceWindow,
			args:        map[string]any{"title": "X"},
			errContains: "startAt and endAt are required",
		},
		{
			name:        "create_maintenance_window rejects malformed startAt",
			tool:        handler.toolCreateMaintenanceWindow,
			args:        map[string]any{"title": "X", "startAt": "yesterday", "endAt": "2026-05-03T23:00:00Z"},
			errContains: "startAt must be RFC3339",
		},
		{
			name:        "create_maintenance_window rejects malformed endAt",
			tool:        handler.toolCreateMaintenanceWindow,
			args:        map[string]any{"title": "X", "startAt": "2026-05-03T22:00:00Z", "endAt": "later"},
			errContains: "endAt must be RFC3339",
		},
		{
			name:        "update_maintenance_window rejects empty uid",
			tool:        handler.toolUpdateMaintenanceWindow,
			args:        map[string]any{},
			errContains: "uid is required",
		},
		{
			name:        "update_maintenance_window rejects malformed startAt",
			tool:        handler.toolUpdateMaintenanceWindow,
			args:        map[string]any{"uid": "u", "startAt": "soon"},
			errContains: "startAt must be RFC3339",
		},
		{
			name:        "delete_maintenance_window rejects empty uid",
			tool:        handler.toolDeleteMaintenanceWindow,
			args:        map[string]any{},
			errContains: "uid is required",
		},
		{
			name:        "set_maintenance_window_checks rejects empty uid",
			tool:        handler.toolSetMaintenanceWindowChecks,
			args:        map[string]any{},
			errContains: "uid is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			result := tc.tool(context.Background(), "test-org", tc.args)
			r.True(result.IsError, "expected error for %s", tc.name)
			r.Contains(result.Content[0].Text, tc.errContains)
		})
	}
}

func TestBuildCreateMaintenanceRequest_Happy(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	args := map[string]any{
		"title":         "DB upgrade",
		"startAt":       "2026-05-03T22:00:00Z",
		"endAt":         "2026-05-03T23:30:00Z",
		"description":   "Upgrade Postgres major version",
		"recurrence":    "FREQ=WEEKLY;BYDAY=SU",
		"recurrenceEnd": "2026-12-31T00:00:00Z",
	}
	req, errMsg := buildCreateMaintenanceRequest(args)
	r.Empty(errMsg)
	r.NotNil(req)
	r.Equal("DB upgrade", req.Title)
	r.NotNil(req.Description)
	r.Equal("Upgrade Postgres major version", *req.Description)
	r.Equal(2026, req.StartAt.Year())
	r.Equal("FREQ=WEEKLY;BYDAY=SU", req.Recurrence)
	r.NotNil(req.RecurrenceEnd)
}

func TestBuildCreateMaintenanceRequest_BadRecurrenceEnd(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	args := map[string]any{
		"title":         "X",
		"startAt":       "2026-05-03T22:00:00Z",
		"endAt":         "2026-05-03T23:00:00Z",
		"recurrenceEnd": "soon",
	}
	req, errMsg := buildCreateMaintenanceRequest(args)
	r.Nil(req)
	r.Contains(errMsg, "recurrenceEnd must be RFC3339")
}

func TestBuildUpdateMaintenanceRequest_PartialPatch(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	args := map[string]any{
		"description": "updated note only",
	}
	req, errMsg := buildUpdateMaintenanceRequest(args)
	r.Empty(errMsg)
	r.NotNil(req)
	r.Nil(req.Title)
	r.NotNil(req.Description)
	r.Equal("updated note only", *req.Description)
	r.Nil(req.StartAt)
	r.Nil(req.EndAt)
	r.Nil(req.Recurrence)
	r.Nil(req.RecurrenceEnd)
}

func TestBuildUpdateMaintenanceRequest_ClearRecurrence(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Caller passes recurrence: "" to explicitly clear
	args := map[string]any{
		"recurrence": "",
	}
	req, errMsg := buildUpdateMaintenanceRequest(args)
	r.Empty(errMsg)
	r.NotNil(req.Recurrence)
	r.Empty(*req.Recurrence)
}
