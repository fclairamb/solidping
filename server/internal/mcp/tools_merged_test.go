package mcp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checktypes"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// Tests for the merged tools of spec 2026-10-12-06: probe, list_check_types
// with type, get_incident / get_status_page include, and manage_status_page_*,
// plus parity with the legacy names they replaced.

// callByName runs a registered tool by name, as tools/call would after the gates.
func callByName(t *testing.T, h *Handler, org, name string, args map[string]any) ToolCallResult {
	t.Helper()

	fn, ok := h.toolMap[name]
	require.Truef(t, ok, "tool %q is not callable", name)

	return fn(t.Context(), org, args)
}

// structuredJSON is the structuredContent of a successful result as generic JSON.
func structuredJSON(t *testing.T, res ToolCallResult) map[string]any {
	t.Helper()
	r := require.New(t)
	r.Falsef(res.IsError, "unexpected tool error: %v", res.Content)

	raw, err := json.Marshal(res.StructuredContent)
	r.NoError(err)

	var out map[string]any
	r.NoError(json.Unmarshal(raw, &out))

	return out
}

func requireToolError(t *testing.T, res ToolCallResult, contains string) {
	t.Helper()
	r := require.New(t)
	r.True(res.IsError, "expected a tool error")
	r.NotEmpty(res.Content)
	r.Contains(res.Content[0].Text, contains)
}

// without drops volatile keys before comparing two writes.
func without(object map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for k, v := range object {
		out[k] = v
	}

	for _, k := range keys {
		delete(out, k)
	}

	return out
}

func newCheckTypesHandlerForTest() *Handler {
	handler := newTestHandler()
	handler.checkTypesSvc = checktypes.NewService(
		checkerdef.NewActivationResolver(&config.CheckersConfig{}, ""), "")

	return handler
}

func newIncidentsServiceForTest(h *Handler) *incidents.Service {
	return incidents.NewService(h.dbService, nil, clock.Real{}, nil)
}

func newMergedEnv(t *testing.T) *coverEnv {
	t.Helper()

	env := newCoverEnv(t)
	env.handler.registerTools()

	return env
}

func TestManageStatusPageSectionAndResource(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMergedEnv(t)
	h, org := env.handler, env.org.Slug

	structuredJSON(t, callByName(t, h, org, toolCreateStatusPage, map[string]any{"name": "Acme", "slug": "acme"}))

	sec := structuredJSON(t, callByName(t, h, org, toolManageStatusPageSection, map[string]any{
		propAction: actionCreate, propPageIdentifier: "acme", "name": "API", "slug": "api",
	}))
	r.Equal("api", sec["slug"])

	upd := structuredJSON(t, callByName(t, h, org, toolManageStatusPageSection, map[string]any{
		propAction: actionUpdate, propPageIdentifier: "acme", propSectionIdentifier: "api", "name": "API 2",
	}))
	r.Equal("API 2", upd["name"])

	res := structuredJSON(t, callByName(t, h, org, toolManageStatusPageResource, map[string]any{
		propAction: actionCreate, propPageIdentifier: "acme", propSectionIdentifier: "api",
		propCheckUID: env.check.UID, propPublicName: "Public API",
	}))
	resUID, ok := res[propUID].(string)
	r.True(ok)

	resUpd := structuredJSON(t, callByName(t, h, org, toolManageStatusPageResource, map[string]any{
		propAction: actionUpdate, propPageIdentifier: "acme", propSectionIdentifier: "api",
		propResourceUID: resUID, propPublicName: "Renamed",
	}))
	r.Equal("Renamed", resUpd["publicName"])

	// get_status_page include: sections and the section's resources.
	page := structuredJSON(t, callByName(t, h, org, toolGetStatusPage, map[string]any{
		propIdentifier: "acme", propInclude: []any{includeSections, includeResources},
		propSectionIdentifier: "api",
	}))
	r.Equal("acme", page["slug"])
	r.Len(page[includeSections], 2, "the default section plus api")
	r.Len(page[includeResources], 1)

	resDel := structuredJSON(t, callByName(t, h, org, toolManageStatusPageResource, map[string]any{
		propAction: actionDelete, propPageIdentifier: "acme", propSectionIdentifier: "api", propResourceUID: resUID,
	}))
	r.Equal(true, resDel[schemaKeyDeleted])

	secDel := structuredJSON(t, callByName(t, h, org, toolManageStatusPageSection, map[string]any{
		propAction: actionDelete, propPageIdentifier: "acme", propSectionIdentifier: "api",
	}))
	r.Equal(true, secDel[schemaKeyDeleted])
}

func TestManageStatusPageIncidentActions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMergedEnv(t)
	h, org, ctx := env.handler, env.org.Slug, t.Context()

	r.NoError(h.dbService.CreateStatusPage(ctx, models.NewStatusPage(env.org.UID, "Public", "public")))
	r.NoError(h.dbService.CreateStatusPage(ctx, models.NewStatusPage(env.org.UID, "Second", "second")))

	pub := structuredJSON(t, callByName(t, h, org, toolManageStatusPageIncident, map[string]any{
		propAction: actionCreate, propPageIdentifier: "public", propTitle: "Acme outage",
	}))
	pubUID, ok := pub[propUID].(string)
	r.True(ok)

	upd := structuredJSON(t, callByName(t, h, org, toolManageStatusPageIncident, map[string]any{
		propAction: actionUpdate, propPageIdentifier: "public", propPublicationUID: pubUID, propState: "identified",
	}))
	r.Equal("identified", upd[propState])

	entry := structuredJSON(t, callByName(t, h, org, toolManageStatusPageIncident, map[string]any{
		propAction: actionAddUpdate, propPageIdentifier: "public", propPublicationUID: pubUID,
		propKind: "monitoring", propBodyMarkdown: "fix deployed",
	}))
	r.Equal("monitoring", entry[propKind])

	page := structuredJSON(t, callByName(t, h, org, toolGetStatusPage, map[string]any{
		propIdentifier: "public", propInclude: []any{includeIncidents}, propActive: true,
	}))
	r.Len(page[includeIncidents], 1)

	linked := structuredJSON(t, callByName(t, h, org, toolManageStatusPageIncident, map[string]any{
		propAction: actionPublish, propIncidentUID: env.incident.UID, propPageIdentifier: "second",
	}))
	linkedUID, ok := linked[propUID].(string)
	r.True(ok)

	gone := structuredJSON(t, callByName(t, h, org, toolManageStatusPageIncident, map[string]any{
		propAction: actionUnpublish, propIncidentUID: env.incident.UID, propPublicationUID: linkedUID,
	}))
	r.Equal(true, gone[schemaKeyUnpublished])
}

// TestManageToolsRefuseBadActions: a missing, mistyped, unknown or
// differently-cased action runs nothing and names the valid actions.
func TestManageToolsRefuseBadActions(t *testing.T) {
	t.Parallel()

	env := newMergedEnv(t)
	h, org := env.handler, env.org.Slug

	manageTools := []string{toolManageStatusPageSection, toolManageStatusPageResource, toolManageStatusPageIncident}
	for _, tool := range manageTools {
		for _, args := range []map[string]any{
			{},
			{propAction: 3},
			{propAction: "frobnicate"},
			{propAction: "Delete"},
		} {
			requireToolError(t, callByName(t, h, org, tool, args), "create")
		}
	}

	requireToolError(t, callByName(t, h, org, toolProbe, map[string]any{propKind: "shell"}), "script, fetch, browser")
}

func TestGetStatusPageInclude(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMergedEnv(t)
	h, org := env.handler, env.org.Slug

	structuredJSON(t, callByName(t, h, org, toolCreateStatusPage, map[string]any{"name": "Acme", "slug": "acme"}))

	plain := structuredJSON(t, callByName(t, h, org, toolGetStatusPage, map[string]any{propIdentifier: "acme"}))
	empty := structuredJSON(t, callByName(t, h, org, toolGetStatusPage, map[string]any{
		propIdentifier: "acme", propInclude: []any{},
	}))
	r.Equal(plain, empty, "include: [] returns the page only")
	r.NotContains(empty, includeSections)

	requireToolError(t, callByName(t, h, org, toolGetStatusPage, map[string]any{
		propIdentifier: "acme", propInclude: []any{"subscribers"},
	}), "unknown include")
	requireToolError(t, callByName(t, h, org, toolGetStatusPage, map[string]any{
		propIdentifier: "acme", propInclude: []any{3},
	}), "array of strings")
	requireToolError(t, callByName(t, h, org, toolGetStatusPage, map[string]any{
		propIdentifier: "acme", propInclude: []any{includeResources},
	}), "sectionIdentifier")
	requireToolError(t, callByName(t, h, org, toolGetStatusPage, map[string]any{
		propIdentifier: "acme", propInclude: []any{includeSections}, propWith: "sections",
	}), "with=sections")
}

// TestLegacyAliasReadParity: each legacy read returns exactly the list the
// merged tool includes.
func TestLegacyAliasReadParity(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMergedEnv(t)
	h, org, ctx := env.handler, env.org.Slug, t.Context()

	r.NoError(h.dbService.CreateStatusPage(ctx, models.NewStatusPage(env.org.UID, "Public", "public")))
	structuredJSON(t, callByName(t, h, org, toolManageStatusPageSection, map[string]any{
		propAction: actionCreate, propPageIdentifier: "public", "name": "API", "slug": "api",
	}))
	structuredJSON(t, callByName(t, h, org, toolManageStatusPageResource, map[string]any{
		propAction: actionCreate, propPageIdentifier: "public", propSectionIdentifier: "api",
		propCheckUID: env.check.UID,
	}))
	structuredJSON(t, callByName(t, h, org, toolManageStatusPageIncident, map[string]any{
		propAction: actionCreate, propPageIdentifier: "public", propTitle: "Acme outage",
	}))

	page := structuredJSON(t, callByName(t, h, org, toolGetStatusPage, map[string]any{
		propIdentifier: "public", propSectionIdentifier: "api",
		propInclude: []any{includeSections, includeResources, includeIncidents},
	}))

	for legacy, include := range map[string]string{
		toolListStatusPageSections:  includeSections,
		toolListStatusPageResources: includeResources,
		toolListStatusPageIncidents: includeIncidents,
	} {
		old := structuredJSON(t, callByName(t, h, org, legacy, map[string]any{
			propPageIdentifier: "public", propSectionIdentifier: "api",
		}))
		r.NotEmpty(old[schemaKeyData], legacy)
		r.Equal(old[schemaKeyData], page[include], legacy)
	}
}

// TestLegacyAliasWriteParity: a legacy write alias and its manage_* action
// produce the same result shape, and the same update lands identically.
func TestLegacyAliasWriteParity(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMergedEnv(t)
	h, org, ctx := env.handler, env.org.Slug, t.Context()

	r.NoError(h.dbService.CreateStatusPage(ctx, models.NewStatusPage(env.org.UID, "Public", "public")))

	viaAlias := structuredJSON(t, callByName(t, h, org, toolCreateStatusPageSection, map[string]any{
		propPageIdentifier: "public", "name": "One", "slug": "one",
	}))
	viaManage := structuredJSON(t, callByName(t, h, org, toolManageStatusPageSection, map[string]any{
		propAction: actionCreate, propPageIdentifier: "public", "name": "Two", "slug": "two",
	}))
	r.Equal(keysOf(viaAlias), keysOf(viaManage))

	volatile := []string{schemaKeyUpdatedAt, schemaKeyCreatedAt}
	update := map[string]any{propPageIdentifier: "public", propSectionIdentifier: "one", "name": "Same"}
	first := structuredJSON(t, callByName(t, h, org, toolUpdateStatusPageSection, update))
	update[propAction] = actionUpdate
	second := structuredJSON(t, callByName(t, h, org, toolManageStatusPageSection, update))
	r.Equal(without(first, volatile...), without(second, volatile...))

	// The alias forces its action: a conflicting action argument is ignored.
	pub := structuredJSON(t, callByName(t, h, org, toolCreateStatusPageIncident, map[string]any{
		propAction: actionUpdate, propPageIdentifier: "public", propTitle: "Acme outage",
	}))
	r.Equal("Acme outage", pub[propTitle])

	for _, alias := range []string{toolDeleteStatusPageSection, toolDeleteStatusPageResource} {
		requireToolError(t, callByName(t, h, org, alias, map[string]any{}), "required")
	}
}

func keysOf(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for k := range object {
		keys = append(keys, k)
	}

	return sortedCopy(keys)
}

func TestGetIncidentIncludeNotifications(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMergedEnv(t)
	h, org := env.handler, env.org.Slug
	h.incidentsSvc = newIncidentsServiceForTest(h)

	args := map[string]any{propUID: env.incident.UID}
	plain := structuredJSON(t, callByName(t, h, org, toolGetIncident, args))
	r.NotContains(plain, includeNotifications)

	args[propInclude] = []any{includeNotifications}
	withNotes := structuredJSON(t, callByName(t, h, org, toolGetIncident, args))
	r.Contains(withNotes, includeNotifications)
	r.Equal(plain[propUID], withNotes[propUID])

	legacy := structuredJSON(t, callByName(t, h, org, toolIncidentNotificationsList, map[string]any{
		propUID: env.incident.UID,
	}))
	r.Equal(legacy[schemaKeyData], withNotes[includeNotifications])

	requireToolError(t, callByName(t, h, org, toolGetIncident, map[string]any{
		propUID: env.incident.UID, propInclude: []any{"events"},
	}), "unknown include")
}

func TestListCheckTypesWithTypeReturnsSamples(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newCheckTypesHandlerForTest()

	types := structuredJSON(t, callByName(t, handler, "org", toolListCheckTypes, nil))
	r.NotEmpty(types[schemaKeyData])

	samples := structuredJSON(t, callByName(t, handler, "org", toolListCheckTypes, map[string]any{schemaKeyType: "http"}))
	legacy := structuredJSON(t, callByName(t, handler, "org", toolGetCheckTypeSamples,
		map[string]any{schemaKeyType: "http"}))
	r.Equal(legacy, samples)
	r.NotEmpty(samples[schemaKeyData])

	requireToolError(t, callByName(t, handler, "org", toolGetCheckTypeSamples, map[string]any{}), "type is required")
}

// TestProbeParity: probe kind=fetch and the legacy fetch_page answer the same.
func TestProbeParity(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler, url := jsAuthoringHandler(t)

	viaProbe := structuredJSON(t, callByName(t, handler, "org", toolProbe,
		map[string]any{propKind: probeKindFetch, propURL: url}))
	viaAlias := structuredJSON(t, callByName(t, handler, "org", toolFetchPage, map[string]any{propURL: url}))
	r.Equal(without(viaAlias, propDurationMs), without(viaProbe, propDurationMs))

	script := map[string]any{propScript: `return { status: "up", metrics: { n: 1 } };`}
	viaAlias = structuredJSON(t, callByName(t, handler, "org", toolRunJSScript, script))
	script[propKind] = probeKindScript
	viaProbe = structuredJSON(t, callByName(t, handler, "org", toolProbe, script))
	r.Equal(without(viaAlias, propDurationMs), without(viaProbe, propDurationMs))
	r.Equal("up", viaProbe["status"])

	// Over HTTP with a read-only token: probe is a read.
	rec, req := makeRequest(t, http.MethodPost,
		toolCall(t, toolProbe, map[string]any{propKind: probeKindFetch, propURL: url}), claimsWithScopes("mcp:read"))
	r.NoError(handler.Handle(rec, req))
	r.Nil(decodeResponse(t, rec).Error)
}

// TestLegacyAliasResourceAndSectionParity: the resource aliases and the
// section delete alias match manage_status_page_resource / _section.
func TestLegacyAliasResourceAndSectionParity(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMergedEnv(t)
	h, org, ctx := env.handler, env.org.Slug, t.Context()

	r.NoError(h.dbService.CreateStatusPage(ctx, models.NewStatusPage(env.org.UID, "Public", "public")))
	for _, slug := range []string{"one", "two"} {
		structuredJSON(t, callByName(t, h, org, toolManageStatusPageSection, map[string]any{
			propAction: actionCreate, propPageIdentifier: "public", "name": slug, "slug": slug,
		}))
	}

	// create: same shape, same public name.
	viaAlias := structuredJSON(t, callByName(t, h, org, toolCreateStatusPageResource, map[string]any{
		propPageIdentifier: "public", propSectionIdentifier: "one",
		propCheckUID: env.check.UID, propPublicName: "Public API",
	}))
	viaManage := structuredJSON(t, callByName(t, h, org, toolManageStatusPageResource, map[string]any{
		propAction: actionCreate, propPageIdentifier: "public", propSectionIdentifier: "two",
		propCheckUID: env.check.UID, propPublicName: "Public API",
	}))
	r.Equal(keysOf(viaAlias), keysOf(viaManage))
	r.Equal(viaAlias[propPublicName], viaManage[propPublicName])

	aliasUID, ok := viaAlias[propUID].(string)
	r.True(ok)
	manageUID, ok := viaManage[propUID].(string)
	r.True(ok)

	// update: the same edit through both names lands identically.
	volatile := []string{schemaKeyUpdatedAt, schemaKeyCreatedAt}
	update := map[string]any{
		propPageIdentifier: "public", propSectionIdentifier: "one",
		propResourceUID: aliasUID, propPublicName: "Renamed",
	}
	first := structuredJSON(t, callByName(t, h, org, toolUpdateStatusPageResource, update))
	r.Equal("Renamed", first[propPublicName])
	update[propAction] = actionUpdate
	second := structuredJSON(t, callByName(t, h, org, toolManageStatusPageResource, update))
	r.Equal(without(first, volatile...), without(second, volatile...))

	// delete: same result shape for each resource.
	delAlias := structuredJSON(t, callByName(t, h, org, toolDeleteStatusPageResource, map[string]any{
		propPageIdentifier: "public", propSectionIdentifier: "one", propResourceUID: aliasUID,
	}))
	delManage := structuredJSON(t, callByName(t, h, org, toolManageStatusPageResource, map[string]any{
		propAction: actionDelete, propPageIdentifier: "public", propSectionIdentifier: "two",
		propResourceUID: manageUID,
	}))
	r.Equal(keysOf(delAlias), keysOf(delManage))
	r.Equal(true, delAlias[schemaKeyDeleted])
	r.Equal(true, delManage[schemaKeyDeleted])

	// delete_status_page_section answers exactly what action=delete answers.
	secAlias := structuredJSON(t, callByName(t, h, org, toolDeleteStatusPageSection, map[string]any{
		propPageIdentifier: "public", propSectionIdentifier: "one",
	}))
	secManage := structuredJSON(t, callByName(t, h, org, toolManageStatusPageSection, map[string]any{
		propAction: actionDelete, propPageIdentifier: "public", propSectionIdentifier: "two",
	}))
	r.Equal(map[string]any{schemaKeyDeleted: true, propIdentifier: "one"}, secAlias)
	r.Equal(map[string]any{schemaKeyDeleted: true, propIdentifier: "two"}, secManage)

	// Both names refuse the same missing arguments the same way.
	for alias, action := range map[string]string{
		toolCreateStatusPageResource: actionCreate,
		toolUpdateStatusPageResource: actionUpdate,
	} {
		old := callByName(t, h, org, alias, map[string]any{})
		merged := callByName(t, h, org, toolManageStatusPageResource, map[string]any{propAction: action})
		r.True(old.IsError, alias)
		r.Equal(old, merged, alias)
	}
}

// TestLegacyAliasIncidentPublicationParity: the incident publication aliases
// match the update, add_update, publish and unpublish actions.
func TestLegacyAliasIncidentPublicationParity(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMergedEnv(t)
	h, org, ctx := env.handler, env.org.Slug, t.Context()

	for _, slug := range []string{"public", "second", "third"} {
		r.NoError(h.dbService.CreateStatusPage(ctx, models.NewStatusPage(env.org.UID, slug, slug)))
	}

	pub := structuredJSON(t, callByName(t, h, org, toolManageStatusPageIncident, map[string]any{
		propAction: actionCreate, propPageIdentifier: "public", propTitle: "Acme outage",
	}))
	pubUID, ok := pub[propUID].(string)
	r.True(ok)

	// update_status_page_incident: the same edit lands identically.
	volatile := []string{schemaKeyUpdatedAt, schemaKeyCreatedAt}
	update := map[string]any{propPageIdentifier: "public", propPublicationUID: pubUID, propState: "identified"}
	first := structuredJSON(t, callByName(t, h, org, toolUpdateStatusPageIncident, update))
	r.Equal("identified", first[propState])
	update[propAction] = actionUpdate
	second := structuredJSON(t, callByName(t, h, org, toolManageStatusPageIncident, update))
	r.Equal(without(first, volatile...), without(second, volatile...))

	// create_status_page_incident_update: same shape, same kind and body.
	entry := map[string]any{
		propPageIdentifier: "public", propPublicationUID: pubUID,
		propKind: "monitoring", propBodyMarkdown: "fix deployed",
	}
	viaAlias := structuredJSON(t, callByName(t, h, org, toolCreateStatusPageIncidentUpdate, entry))
	entry[propAction] = actionAddUpdate
	viaManage := structuredJSON(t, callByName(t, h, org, toolManageStatusPageIncident, entry))
	r.Equal(keysOf(viaAlias), keysOf(viaManage))
	r.Equal(viaAlias[propKind], viaManage[propKind])
	r.NotEqual(viaAlias[propUID], viaManage[propUID], "two distinct updates were added")

	// create_incident_publication: same shape on two pages.
	linkedAlias := structuredJSON(t, callByName(t, h, org, toolCreateIncidentPublication, map[string]any{
		propIncidentUID: env.incident.UID, propPageIdentifier: "second",
	}))
	linkedManage := structuredJSON(t, callByName(t, h, org, toolManageStatusPageIncident, map[string]any{
		propAction: actionPublish, propIncidentUID: env.incident.UID, propPageIdentifier: "third",
	}))
	r.Equal(keysOf(linkedAlias), keysOf(linkedManage))
	r.Equal(linkedAlias[propTitle], linkedManage[propTitle])

	aliasUID, ok := linkedAlias[propUID].(string)
	r.True(ok)
	manageUID, ok := linkedManage[propUID].(string)
	r.True(ok)

	// delete_incident_publication answers exactly what action=unpublish answers.
	goneAlias := structuredJSON(t, callByName(t, h, org, toolDeleteIncidentPublication, map[string]any{
		propIncidentUID: env.incident.UID, propPublicationUID: aliasUID,
	}))
	goneManage := structuredJSON(t, callByName(t, h, org, toolManageStatusPageIncident, map[string]any{
		propAction: actionUnpublish, propIncidentUID: env.incident.UID, propPublicationUID: manageUID,
	}))
	r.Equal(map[string]any{schemaKeyUnpublished: true, propPublicationUID: aliasUID}, goneAlias)
	r.Equal(map[string]any{schemaKeyUnpublished: true, propPublicationUID: manageUID}, goneManage)

	// Both names refuse the same missing arguments the same way.
	for alias, action := range map[string]string{
		toolUpdateStatusPageIncident:       actionUpdate,
		toolCreateStatusPageIncidentUpdate: actionAddUpdate,
		toolCreateIncidentPublication:      actionPublish,
		toolDeleteIncidentPublication:      actionUnpublish,
	} {
		old := callByName(t, h, org, alias, map[string]any{})
		merged := callByName(t, h, org, toolManageStatusPageIncident, map[string]any{propAction: action})
		r.True(old.IsError, alias)
		r.Equal(old, merged, alias)
	}
}

// TestBrowserSnapshotAliasParity: there is no browser fixture in unit tests,
// so parity is checked on the path that runs without one: a missing URL is
// refused identically by browser_snapshot and probe kind=browser.
func TestBrowserSnapshotAliasParity(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler, _ := jsAuthoringHandler(t)

	old := callByName(t, handler, "org", toolBrowserSnapshot, map[string]any{})
	merged := callByName(t, handler, "org", toolProbe, map[string]any{propKind: probeKindBrowser})
	r.True(old.IsError)
	r.Equal(old, merged)
}
