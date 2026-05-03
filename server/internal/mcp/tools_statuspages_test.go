package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatusPageToolDefinitions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defs := []ToolDefinition{
		listStatusPagesDef(),
		getStatusPageDef(),
		createStatusPageDef(),
		updateStatusPageDef(),
		deleteStatusPageDef(),
		listStatusPageSectionsDef(),
		createStatusPageSectionDef(),
		updateStatusPageSectionDef(),
		deleteStatusPageSectionDef(),
		listStatusPageResourcesDef(),
		createStatusPageResourceDef(),
		updateStatusPageResourceDef(),
		deleteStatusPageResourceDef(),
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
			errContains: "pageIdentifier, sectionIdentifier, and checkUid are required",
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
		"name":             "New name",
		"slug":             "new-slug",
		"description":      "details",
		"visibility":       "public",
		"isDefault":        true,
		"enabled":          false,
		"showAvailability": true,
		"showResponseTime": false,
		"historyDays":      float64(30),
		"language":         "fr",
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
