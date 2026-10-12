package mcp

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// defaultToolNames is the exact default tools/list (spec 2026-10-12-06).
func defaultToolNames() []string {
	return []string{
		// core
		toolListChecks, toolGetCheck, toolCreateCheck, toolUpdateCheck, toolDeleteCheck,
		toolValidateCheck, toolRunCheck, toolDiagnoseCheck, toolListResults, toolListIncidents,
		toolGetIncident, toolListCheckTypes, toolListRegions, toolListCheckGroups, toolProbe,
		// statuspages
		toolListStatusPages, toolGetStatusPage, toolCreateStatusPage, toolUpdateStatusPage,
		toolDeleteStatusPage, toolManageStatusPageSection, toolManageStatusPageResource,
		toolManageStatusPageIncident,
	}
}

// legacyToolNames are the replaced names, callable and listed only under legacy.
func legacyToolNames() []string {
	return []string{
		toolRunJSScript, toolFetchPage, toolBrowserSnapshot, toolGetCheckTypeSamples,
		toolIncidentNotificationsList,
		toolListStatusPageSections, toolCreateStatusPageSection, toolUpdateStatusPageSection,
		toolDeleteStatusPageSection,
		toolListStatusPageResources, toolCreateStatusPageResource, toolUpdateStatusPageResource,
		toolDeleteStatusPageResource,
		toolListStatusPageIncidents, toolCreateStatusPageIncident, toolUpdateStatusPageIncident,
		toolCreateStatusPageIncidentUpdate, toolCreateIncidentPublication, toolDeleteIncidentPublication,
	}
}

func sortedCopy(names []string) []string {
	out := slices.Clone(names)
	sort.Strings(out)

	return out
}

// toolsListOverHTTP runs tools/list through Handle with the given query string.
func toolsListOverHTTP(t *testing.T, handler *Handler, query string) Response {
	t.Helper()

	rec, req := makeRequest(t, http.MethodPost, `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`, defaultClaims())
	req.URL.RawQuery = query
	require.NoError(t, handler.Handle(rec, req))

	return decodeResponse(t, rec)
}

func toolsListNames(t *testing.T, resp Response) []string {
	t.Helper()
	r := require.New(t)
	r.Nil(resp.Error)

	result, ok := resp.Result.(map[string]any)
	r.True(ok)

	tools, ok := result["tools"].([]any)
	r.True(ok)

	names := make([]string, 0, len(tools))

	for _, tool := range tools {
		def, isMap := tool.(map[string]any)
		r.True(isMap)

		name, _ := def[schemaKeyName].(string)
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func TestDefaultToolListIsSmall(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	names := toolsListNames(t, toolsListOverHTTP(t, handler, ""))

	r.Equal(sortedCopy(defaultToolNames()), names)
	r.LessOrEqual(len(names), 25, "the default list must stay at or under 25 tools")
}

func TestToolsetSelectionOverHTTP(t *testing.T) {
	t.Parallel()

	allNames := slices.Concat(defaultToolNames(), []string{
		toolListIntegrations, toolCreateIntegration, toolListMaintenanceWindows,
		toolGetMaintenanceWindow, toolCreateMaintenanceWindow, toolUpdateMaintenanceWindow,
		toolDeleteMaintenanceWindow, toolSetMaintenanceWindowCheck,
	})

	cases := []struct {
		query string
		want  []string
	}{
		{query: "", want: defaultToolNames()},
		{query: "toolsets=core,statuspages", want: defaultToolNames()},
		{query: "toolsets=all", want: allNames},
		{query: "toolsets=core,statuspages,admin", want: allNames},
		{query: "toolsets=all,legacy", want: slices.Concat(allNames, legacyToolNames())},
		{query: "toolsets=legacy", want: legacyToolNames()},
		{query: "toolsets=%20core%20,", want: defaultToolNames()[:15]},
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			names := toolsListNames(t, toolsListOverHTTP(t, newTestHandler(), tc.query))
			r.Equal(sortedCopy(tc.want), names)
		})
	}

	require.Len(t, allNames, 31)
	require.Len(t, legacyToolNames(), 19)
}

func TestUnknownToolsetIsInvalidParams(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, query := range []string{"toolsets=bogus", "toolsets=core,Admin", "toolsets=core,bogus"} {
		resp := toolsListOverHTTP(t, newTestHandler(), query)
		r.NotNil(resp.Error, query)
		r.Equal(CodeInvalidParams, resp.Error.Code, query)
		r.Contains(resp.Error.Message, "core, statuspages, admin, legacy, all", query)
	}

	// Positive control: a valid list is not refused.
	r.Nil(toolsListOverHTTP(t, newTestHandler(), "toolsets=admin").Error)
}

func TestParseToolsets(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sel, err := parseToolsets("")
	r.NoError(err)
	r.Equal(defaultToolsets(), sel)

	sel, err = parseToolsets(" , ")
	r.NoError(err)
	r.Equal(defaultToolsets(), sel)

	sel, err = parseToolsets("all")
	r.NoError(err)
	r.True(sel.has(toolsetAdmin))
	r.False(sel.has(toolsetLegacy), "all does not include the deprecated aliases")

	_, err = parseToolsets("nope")
	r.ErrorIs(err, errUnknownToolset)
	r.Error(ValidateToolsets("nope"))
	r.NoError(ValidateToolsets("core"))
}

// TestEveryToolHasAToolset: a tool registered without a known toolset tag
// would silently vanish from every listing.
func TestEveryToolHasAToolset(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	known := []string{toolsetCore, toolsetStatusPages, toolsetAdmin, toolsetLegacy}

	r.Len(handler.toolsets, len(handler.toolMap))

	for name := range handler.toolMap {
		r.Containsf(known, handler.toolsets[name], "tool %q has no known toolset", name)
	}

	for _, name := range legacyToolNames() {
		r.Equal(toolsetLegacy, handler.toolsets[name], name)
	}
}

// TestUnlistedToolStaysCallableAndGated: the selector shapes the list, never
// authorization. A legacy alias and a tool outside the selected toolsets are
// called through Handle; a read-only token is refused on the write ones.
func TestUnlistedToolStaysCallableAndGated(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()

	reached := map[string]bool{}

	for _, name := range []string{toolListIntegrations, toolCreateIntegration, toolDeleteStatusPageSection} {
		handler.toolMap[name] = func(context.Context, string, map[string]any) ToolCallResult {
			reached[name] = true

			return textResult("stub")
		}
	}

	call := func(tool string, claims *auth.Claims) Response {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":{}}}`
		rec, req := makeRequest(t, http.MethodPost, body, claims)
		req.URL.RawQuery = "toolsets=core"
		r.NoError(handler.Handle(rec, req))

		return decodeResponse(t, rec)
	}

	readOnly := &auth.Claims{UserUID: "u", OrgSlug: "test-org", Scopes: []string{scopeMCPRead}}

	// Read tool outside the selection: callable.
	r.Nil(call(toolListIntegrations, readOnly).Error)
	r.True(reached[toolListIntegrations])

	// Write tool outside the selection and a legacy write alias: refused by
	// the scope gate exactly as when listed, before running.
	for _, tool := range []string{toolCreateIntegration, toolDeleteStatusPageSection} {
		resp := call(tool, readOnly)
		r.NotNil(resp.Error, tool)
		r.Equal(CodeForbidden, resp.Error.Code, tool)
		r.False(reached[tool], tool)
	}
}
