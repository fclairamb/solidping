package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaintenanceWindowToolDefinitions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		def         ToolDefinition
		title       string
		readOnly    bool
		destructive bool
		idempotent  bool
	}{
		{listMaintenanceWindowsDef(), "List maintenance windows", true, false, true},
		{getMaintenanceWindowDef(), "Get maintenance window", true, false, true},
		{createMaintenanceWindowDef(), "Create maintenance window", false, false, false},
		{updateMaintenanceWindowDef(), "Update maintenance window", false, false, true},
		{deleteMaintenanceWindowDef(), "Delete maintenance window", false, true, true},
		{setMaintenanceWindowChecksDef(), "Set maintenance window checks", false, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.def.Name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			def := tc.def
			r.NotEmpty(def.Name)
			r.NotEmpty(def.Description)
			r.NotNil(def.InputSchema)

			r.NotNil(def.Annotations, "%s must declare annotations", def.Name)
			r.Equal(tc.title, def.Annotations.Title, "sentence-case title")
			r.Equal(tc.readOnly, def.Annotations.ReadOnlyHint, def.Name)
			r.Equal(tc.destructive, def.Annotations.DestructiveHint, def.Name)
			r.Equal(tc.idempotent, def.Annotations.IdempotentHint, def.Name)
			r.False(def.Annotations.OpenWorldHint, def.Name)

			if tc.readOnly {
				r.Contains(def.Description, "Read-only: works with mcp:read tokens.")
			} else {
				r.Contains(def.Description,
					"Requires the mcp scope (mcp:read tokens are refused) and at least the "+
						"user role in the organization.")
			}

			r.NotNil(def.OutputSchema, "%s must declare an outputSchema", def.Name)
			schema, ok := def.OutputSchema.(map[string]any)
			r.True(ok, "%s outputSchema must be an object schema", def.Name)
			r.Equal(schemaTypeObject, schema[schemaKeyType])
			props, hasProps := schema[schemaKeyProperties].(map[string]any)
			r.True(hasProps, "%s outputSchema must declare properties", def.Name)
			r.NotEmpty(props)
		})
	}
}

// TestMaintenanceWindowWriteResultsAreStructured locks the two formerly
// text-only results (delete, set checks) to structuredContent objects that
// match their declared outputSchemas.
func TestMaintenanceWindowWriteResultsAreStructured(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	deleteSchema, ok := deleteMaintenanceWindowDef().OutputSchema.(map[string]any)
	r.True(ok)
	deleteProps, ok := deleteSchema[schemaKeyProperties].(map[string]any)
	r.True(ok)
	for _, key := range []string{schemaKeyDeleted, propUID} {
		r.Contains(deleteProps, key, "delete outputSchema must declare %q", key)
	}

	setSchema, ok := setMaintenanceWindowChecksDef().OutputSchema.(map[string]any)
	r.True(ok)
	setProps, ok := setSchema[schemaKeyProperties].(map[string]any)
	r.True(ok)
	for _, key := range []string{schemaKeyUpdated, propCheckUIDs, propCheckGroupUIDs} {
		r.Contains(setProps, key, "set outputSchema must declare %q", key)
	}
}

func TestMaintenanceWindowDescriptionsIncludeExamples(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	def := createMaintenanceWindowDef()
	schema, ok := def.InputSchema.(map[string]any)
	r.True(ok)
	props, ok := schema[schemaKeyProperties].(map[string]any)
	r.True(ok)

	// Recurrence description must document the real enum, NOT iCalendar RRULE — the
	// service only accepts none|daily|weekly|monthly, so advertising RRULE breaks LLMs.
	rec, ok := props[propRecurrence].(map[string]any)
	r.True(ok)
	desc, ok := rec[schemaKeyDescription].(string)
	r.True(ok)
	r.Contains(desc, "daily")
	r.Contains(desc, "weekly")
	r.Contains(desc, "monthly")
	r.NotContains(desc, "FREQ=", "recurrence docs must not advertise iCalendar RRULE")

	// startAt and endAt must show concrete RFC3339 examples
	for _, key := range []string{propStartAt, propEndAt} {
		prop, ok := props[key].(map[string]any)
		r.True(ok)
		desc, ok := prop[schemaKeyDescription].(string)
		r.True(ok)
		r.Contains(desc, "2026-")
	}
}

// TestUpdateMaintenanceRecurrenceDocsNoRRULE locks the update tool's recurrence
// docs to the enum too (it previously advertised RRULE).
func TestUpdateMaintenanceRecurrenceDocsNoRRULE(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	def := updateMaintenanceWindowDef()
	schema, ok := def.InputSchema.(map[string]any)
	r.True(ok)
	props, ok := schema[schemaKeyProperties].(map[string]any)
	r.True(ok)

	rec, ok := props[propRecurrence].(map[string]any)
	r.True(ok)
	desc, ok := rec[schemaKeyDescription].(string)
	r.True(ok)
	r.NotContains(desc, "FREQ=", "update recurrence docs must not advertise iCalendar RRULE")
	r.Contains(desc, "monthly")
	// The service rejects recurrence "" (ErrInvalidRecurrence), so the docs
	// must route clearing through "none", never through an empty string.
	r.Contains(desc, "\"none\"")
	r.NotContains(desc, "empty string")
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
			args:        map[string]any{propStartAt: "2026-05-03T22:00:00Z", propEndAt: "2026-05-03T23:00:00Z"},
			errContains: "title is required",
		},
		{
			name:        "create_maintenance_window rejects missing time bounds",
			tool:        handler.toolCreateMaintenanceWindow,
			args:        map[string]any{propTitle: "X"},
			errContains: "startAt and endAt are required",
		},
		{
			name:        "create_maintenance_window rejects malformed startAt",
			tool:        handler.toolCreateMaintenanceWindow,
			args:        map[string]any{propTitle: "X", propStartAt: "yesterday", propEndAt: "2026-05-03T23:00:00Z"},
			errContains: "startAt must be RFC3339",
		},
		{
			name:        "create_maintenance_window rejects malformed endAt",
			tool:        handler.toolCreateMaintenanceWindow,
			args:        map[string]any{propTitle: "X", propStartAt: "2026-05-03T22:00:00Z", propEndAt: "later"},
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
			args:        map[string]any{propUID: "u", propStartAt: "soon"},
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
		propTitle:            "DB upgrade",
		propStartAt:          "2026-05-03T22:00:00Z",
		propEndAt:            "2026-05-03T23:30:00Z",
		schemaKeyDescription: "Upgrade Postgres major version",
		"recurrence":         "weekly",
		"recurrenceEnd":      "2026-12-31T00:00:00Z",
	}
	req, errMsg := buildCreateMaintenanceRequest(args)
	r.Empty(errMsg)
	r.NotNil(req)
	r.Equal("DB upgrade", req.Title)
	r.NotNil(req.Description)
	r.Equal("Upgrade Postgres major version", *req.Description)
	r.Equal(2026, req.StartAt.Year())
	r.Equal("weekly", req.Recurrence)
	r.NotNil(req.RecurrenceEnd)
}

func TestBuildCreateMaintenanceRequest_BadRecurrenceEnd(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	args := map[string]any{
		propTitle:       "X",
		propStartAt:     "2026-05-03T22:00:00Z",
		propEndAt:       "2026-05-03T23:00:00Z",
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
		schemaKeyDescription: "updated note only",
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

	// The documented way to clear recurrence on update is "none" — the service
	// rejects any value outside none|daily|weekly|monthly, including "".
	args := map[string]any{
		"recurrence": "none",
	}
	req, errMsg := buildUpdateMaintenanceRequest(args)
	r.Empty(errMsg)
	r.NotNil(req.Recurrence)
	r.Equal("none", *req.Recurrence)
}
