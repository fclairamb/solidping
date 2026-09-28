package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type statusPageToolCase struct {
	def             ToolDefinition
	wantTitle       string
	wantReadOnly    bool
	wantDestructive bool
}

func statusPageToolCases() []statusPageToolCase {
	return []statusPageToolCase{
		{listStatusPagesDef(), "List status pages", true, false},
		{getStatusPageDef(), "Get status page", true, false},
		{createStatusPageDef(), "Create status page", false, false},
		{updateStatusPageDef(), "Update status page", false, false},
		{deleteStatusPageDef(), "Delete status page", false, true},
		{listStatusPageSectionsDef(), "List status page sections", true, false},
		{createStatusPageSectionDef(), "Create status page section", false, false},
		{updateStatusPageSectionDef(), "Update status page section", false, false},
		{deleteStatusPageSectionDef(), "Delete status page section", false, true},
		{listStatusPageResourcesDef(), "List status page resources", true, false},
		{createStatusPageResourceDef(), "Create status page resource", false, false},
		{updateStatusPageResourceDef(), "Update status page resource", false, false},
		{deleteStatusPageResourceDef(), "Delete status page resource", false, true},
	}
}

func TestStatusPageToolDefinitions(t *testing.T) {
	t.Parallel()

	for _, tc := range statusPageToolCases() {
		t.Run(tc.def.Name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			r.NotEmpty(tc.def.Name)
			r.NotEmpty(tc.def.Description)
			r.NotNil(tc.def.InputSchema)

			r.NotNil(tc.def.Annotations, "%s must declare annotations", tc.def.Name)
			r.Equal(tc.wantTitle, tc.def.Annotations.Title)
			r.Equal(tc.wantReadOnly, tc.def.Annotations.ReadOnlyHint)
			r.Equal(tc.wantDestructive, tc.def.Annotations.DestructiveHint)
			r.False(tc.def.Annotations.OpenWorldHint)

			r.NotNil(tc.def.OutputSchema, "%s must declare an output schema", tc.def.Name)
			schema, ok := tc.def.OutputSchema.(map[string]any)
			r.True(ok, "%s output schema must be an object schema", tc.def.Name)
			r.Equal(schemaTypeObject, schema[schemaKeyType])
		})
	}
}

// TestStatusPageToolDescriptionsDiscloseAuth pins the exact auth sentences the
// tool descriptions are required to carry.
func TestStatusPageToolDescriptionsDiscloseAuth(t *testing.T) {
	t.Parallel()

	const readSentence = "Read-only: works with mcp:read tokens."
	const writeSentence = "Requires the mcp scope (mcp:read tokens are refused) and at least the " +
		"user role in the organization."

	for _, tc := range statusPageToolCases() {
		t.Run(tc.def.Name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			if tc.wantReadOnly {
				r.Contains(tc.def.Description, readSentence)
				return
			}

			r.Contains(tc.def.Description, writeSentence)
		})
	}
}

func TestStatusPageRequiredArgs(t *testing.T) {
	t.Parallel()

	handler := newTestHandler()

	tests := []struct {
		name        string
		tool        toolFunc
		args        map[string]any
		errContains string
	}{
		{
			name:        "get_status_page rejects empty identifier",
			tool:        handler.toolGetStatusPage,
			args:        map[string]any{},
			errContains: "identifier is required",
		},
		{
			name:        "create_status_page rejects missing name and slug",
			tool:        handler.toolCreateStatusPage,
			args:        map[string]any{},
			errContains: "name and slug are required",
		},
		{
			name:        "update_status_page rejects empty identifier",
			tool:        handler.toolUpdateStatusPage,
			args:        map[string]any{},
			errContains: "identifier is required",
		},
		{
			name:        "delete_status_page rejects empty identifier",
			tool:        handler.toolDeleteStatusPage,
			args:        map[string]any{},
			errContains: "identifier is required",
		},
		{
			name:        "list_status_page_sections rejects missing pageIdentifier",
			tool:        handler.toolListStatusPageSections,
			args:        map[string]any{},
			errContains: "pageIdentifier is required",
		},
		{
			name:        "create_status_page_section rejects missing args",
			tool:        handler.toolCreateStatusPageSection,
			args:        map[string]any{},
			errContains: "pageIdentifier, name, and slug are required",
		},
		{
			name:        "update_status_page_section rejects missing identifiers",
			tool:        handler.toolUpdateStatusPageSection,
			args:        map[string]any{"pageIdentifier": "p"},
			errContains: "pageIdentifier and sectionIdentifier are required",
		},
		{
			name:        "delete_status_page_section rejects missing identifiers",
			tool:        handler.toolDeleteStatusPageSection,
			args:        map[string]any{},
			errContains: "pageIdentifier and sectionIdentifier are required",
		},
		{
			name:        "list_status_page_resources rejects missing identifiers",
			tool:        handler.toolListStatusPageResources,
			args:        map[string]any{},
			errContains: "pageIdentifier and sectionIdentifier are required",
		},
		{
			name:        "create_status_page_resource rejects missing args",
			tool:        handler.toolCreateStatusPageResource,
			args:        map[string]any{},
			errContains: "pageIdentifier and sectionIdentifier are required",
		},
		{
			// A resource targets exactly one of a check or a check group
			// (spec 2026-08-01-03); neither is as invalid as both.
			name:        "create_status_page_resource rejects a missing target",
			tool:        handler.toolCreateStatusPageResource,
			args:        map[string]any{"pageIdentifier": "public", "sectionIdentifier": "core"},
			errContains: "exactly one of checkUid or checkGroupUid is required",
		},
		{
			name: "create_status_page_resource rejects both targets",
			tool: handler.toolCreateStatusPageResource,
			args: map[string]any{
				"pageIdentifier": "public", "sectionIdentifier": "core",
				propCheckUID: "api", propCheckGroupUID: "web-frontend",
			},
			errContains: "exactly one of checkUid or checkGroupUid is required",
		},
		{
			name:        "update_status_page_resource rejects missing args",
			tool:        handler.toolUpdateStatusPageResource,
			args:        map[string]any{},
			errContains: "pageIdentifier, sectionIdentifier, and resourceUid are required",
		},
		{
			name:        "delete_status_page_resource rejects missing args",
			tool:        handler.toolDeleteStatusPageResource,
			args:        map[string]any{},
			errContains: "pageIdentifier, sectionIdentifier, and resourceUid are required",
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

func TestBuildUpdateStatusPageRequest_PassThrough(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	args := map[string]any{
		schemaKeyName:        "New name",
		schemaKeySlug:        "new-slug",
		schemaKeyDescription: "details",
		"visibility":         "public",
		propIsDefault:        true,
		schemaKeyEnabled:     false,
		"showAvailability":   true,
		"showResponseTime":   false,
		"historyDays":        float64(30),
		"language":           "fr",
	}
	req := buildUpdateStatusPageRequest(args)

	r.NotNil(req.Name)
	r.Equal("New name", *req.Name)
	r.NotNil(req.Slug)
	r.Equal("new-slug", *req.Slug)
	r.NotNil(req.Description)
	r.Equal("details", *req.Description)
	r.NotNil(req.Visibility)
	r.Equal("public", *req.Visibility)
	r.NotNil(req.IsDefault)
	r.True(*req.IsDefault)
	r.NotNil(req.Enabled)
	r.False(*req.Enabled)
	r.NotNil(req.ShowAvailability)
	r.True(*req.ShowAvailability)
	r.NotNil(req.ShowResponseTime)
	r.False(*req.ShowResponseTime)
	r.NotNil(req.HistoryDays)
	r.Equal(30, *req.HistoryDays)
	r.NotNil(req.Language)
	r.Equal("fr", *req.Language)
}

func TestBuildUpdateStatusPageRequest_OmittedFieldsStayNil(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	req := buildUpdateStatusPageRequest(map[string]any{})
	r.Nil(req.Name)
	r.Nil(req.Slug)
	r.Nil(req.Description)
	r.Nil(req.Visibility)
	r.Nil(req.IsDefault)
	r.Nil(req.Enabled)
	r.Nil(req.ShowAvailability)
	r.Nil(req.ShowResponseTime)
	r.Nil(req.HistoryDays)
	r.Nil(req.Language)
}
