package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
)

type coverEnv struct {
	handler  *Handler
	org      *models.Organization
	check    *models.Check
	incident *models.Incident
}

func newCoverEnv(t *testing.T) *coverEnv {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("covorg", "Acme Cover Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "acme-api", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, time.Now(), "acme api down")
	r.NoError(dbSvc.CreateIncident(ctx, incident))

	handler := &Handler{dbService: dbSvc}
	handler.statusPagesSvc = statuspages.NewService(dbSvc, nil, nil)
	handler.publicationsSvc = incidentpublications.NewService(dbSvc, nil, nil)

	return &coverEnv{handler: handler, org: org, check: check, incident: incident}
}

func decodeResult(t *testing.T, res ToolCallResult) map[string]any {
	t.Helper()
	r := require.New(t)
	r.False(res.IsError, "unexpected error: %v", res.Content)
	r.NotEmpty(res.Content)
	var out map[string]any
	r.NoError(json.Unmarshal([]byte(res.Content[0].Text), &out))
	return out
}

func TestStatusPageToolsLifecycle(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newCoverEnv(t)
	ctx := t.Context()
	h := env.handler
	org := env.org.Slug

	created := decodeResult(t, h.toolCreateStatusPage(ctx, org, map[string]any{
		"name": "Acme Status", "slug": "acme", "description": "d", "visibility": "public",
		propHistoryDays: float64(30), propLanguage: "en", propCustomCSS: "",
		propAutoPublish: true, propAutoPublishDelay: float64(60), propAutoResolve: "always",
		propShowAvailability: true, propShowResponseTime: true, propIsDefault: true,
	}))
	r.Equal("acme", created["slug"])

	list := decodeResult(t, h.toolListStatusPages(ctx, org, nil))
	r.Len(list["data"], 1)

	got := decodeResult(t, h.toolGetStatusPage(ctx, org, map[string]any{propIdentifier: "acme", propWith: "sections"}))
	r.Equal("acme", got["slug"])

	upd := decodeResult(t, h.toolUpdateStatusPage(ctx, org, map[string]any{
		propIdentifier: "acme", "name": "Acme Status 2",
	}))
	r.Equal("Acme Status 2", upd["name"])

	// sections
	sec := decodeResult(t, h.toolCreateStatusPageSection(ctx, org, map[string]any{
		propPageIdentifier: "acme", "name": "API", "slug": "api", propPosition: float64(1),
	}))
	r.Equal("api", sec["slug"])

	secs := decodeResult(t, h.toolListStatusPageSections(ctx, org, map[string]any{propPageIdentifier: "acme"}))
	r.Len(secs["data"], 2)

	secUpd := decodeResult(t, h.toolUpdateStatusPageSection(ctx, org, map[string]any{
		propPageIdentifier: "acme", propSectionIdentifier: "api",
		"name": "API 2", "slug": "api2", propPosition: float64(2),
	}))
	r.Equal("API 2", secUpd["name"])

	// resources
	res := decodeResult(t, h.toolCreateStatusPageResource(ctx, org, map[string]any{
		propPageIdentifier: "acme", propSectionIdentifier: "api2",
		propCheckUID: env.check.UID, propPublicName: "Public API",
		propExplanation: "main api", propPosition: float64(1),
	}))
	resUID, ok := res["uid"].(string)
	r.True(ok)

	resList := decodeResult(t, h.toolListStatusPageResources(ctx, org, map[string]any{
		propPageIdentifier: "acme", propSectionIdentifier: "api2",
	}))
	r.Len(resList["data"], 1)

	resUpd := decodeResult(t, h.toolUpdateStatusPageResource(ctx, org, map[string]any{
		propPageIdentifier: "acme", propSectionIdentifier: "api2", propResourceUID: resUID,
		propPublicName: "Renamed", propExplanation: "x", propPosition: float64(3),
	}))
	r.Equal("Renamed", resUpd["publicName"])

	del := decodeResult(t, h.toolDeleteStatusPageResource(ctx, org, map[string]any{
		propPageIdentifier: "acme", propSectionIdentifier: "api2", propResourceUID: resUID,
	}))
	r.Equal(true, del["deleted"])

	delSec := decodeResult(t, h.toolDeleteStatusPageSection(ctx, org, map[string]any{
		propPageIdentifier: "acme", propSectionIdentifier: "api2",
	}))
	r.Equal(true, delSec["deleted"])

	delPage := decodeResult(t, h.toolDeleteStatusPage(ctx, org, map[string]any{propIdentifier: "acme"}))
	r.Equal(true, delPage["deleted"])
}

func TestStatusPageToolsServiceErrors(t *testing.T) {
	t.Parallel()
	env := newCoverEnv(t)
	h := env.handler
	org := env.org.Slug

	tests := []struct {
		name string
		tool toolFunc
		args map[string]any
	}{
		{"get missing page", h.toolGetStatusPage, map[string]any{propIdentifier: "nope"}},
		{"update missing page", h.toolUpdateStatusPage, map[string]any{propIdentifier: "nope", "name": "x"}},
		{"delete missing page", h.toolDeleteStatusPage, map[string]any{propIdentifier: "nope"}},
		{"list sections missing page", h.toolListStatusPageSections, map[string]any{propPageIdentifier: "nope"}},
		{"create section missing page", h.toolCreateStatusPageSection,
			map[string]any{propPageIdentifier: "nope", "name": "a", "slug": "a"}},
		{"update section missing page", h.toolUpdateStatusPageSection,
			map[string]any{propPageIdentifier: "nope", propSectionIdentifier: "a"}},
		{"delete section missing page", h.toolDeleteStatusPageSection,
			map[string]any{propPageIdentifier: "nope", propSectionIdentifier: "a"}},
		{"list resources missing page", h.toolListStatusPageResources,
			map[string]any{propPageIdentifier: "nope", propSectionIdentifier: "a"}},
		{"create resource missing page", h.toolCreateStatusPageResource,
			map[string]any{propPageIdentifier: "nope", propSectionIdentifier: "a", propCheckUID: "c"}},
		{"create resource both targets", h.toolCreateStatusPageResource,
			map[string]any{propPageIdentifier: "p", propSectionIdentifier: "a", propCheckUID: "c", propCheckGroupUID: "g"}},
		{"create resource missing page/section", h.toolCreateStatusPageResource, map[string]any{}},
		{"update resource missing page", h.toolUpdateStatusPageResource,
			map[string]any{propPageIdentifier: "nope", propSectionIdentifier: "a", propResourceUID: "r"}},
		{"update resource missing args", h.toolUpdateStatusPageResource, map[string]any{}},
		{"delete resource missing page", h.toolDeleteStatusPageResource,
			map[string]any{propPageIdentifier: "nope", propSectionIdentifier: "a", propResourceUID: "r"}},
		{"delete resource missing args", h.toolDeleteStatusPageResource, map[string]any{}},
		{"create page invalid slug", h.toolCreateStatusPage, map[string]any{"name": "x", "slug": "Bad Slug!"}},
		{"update section missing args", h.toolUpdateStatusPageSection, map[string]any{}},
		{"delete section missing args", h.toolDeleteStatusPageSection, map[string]any{}},
		{"list resources missing args", h.toolListStatusPageResources, map[string]any{}},
		{"create section missing args", h.toolCreateStatusPageSection, map[string]any{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			res := tc.tool(context.Background(), org, tc.args)
			r.True(res.IsError)
			r.NotEmpty(res.Content)
		})
	}
}

func TestIncidentPublicationToolsLifecycle(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newCoverEnv(t)
	ctx := t.Context()
	h := env.handler
	org := env.org.Slug

	r.NoError(h.dbService.CreateStatusPage(ctx, models.NewStatusPage(env.org.UID, "Public", "public")))
	r.NoError(h.dbService.CreateStatusPage(ctx, models.NewStatusPage(env.org.UID, "Second", "second")))

	pub := decodeResult(t, h.toolCreateStatusPageIncident(ctx, org, map[string]any{
		propPageIdentifier: "public", propTitle: "Acme outage", propState: "investigating",
		propSeverity: "major", propBodyMarkdown: "looking",
	}))
	pubUID, ok := pub["uid"].(string)
	r.True(ok)

	upd := decodeResult(t, h.toolUpdateStatusPageIncident(ctx, org, map[string]any{
		propPageIdentifier: "public", propPublicationUID: pubUID,
		propTitle: "Acme outage 2", propState: "identified", propSeverity: "critical",
	}))
	r.Equal("Acme outage 2", upd["title"])

	au := decodeResult(t, h.toolCreateStatusPageIncidentUpdate(ctx, org, map[string]any{
		propPageIdentifier: "public", propPublicationUID: pubUID,
		propKind: "monitoring", propBodyMarkdown: "fix deployed", propTitle: "Fix",
	}))
	r.Equal("monitoring", au["kind"])

	list := decodeResult(t, h.toolListStatusPageIncidents(ctx, org, map[string]any{
		propPageIdentifier: "public", propActive: true, propState: "monitoring",
	}))
	r.Len(list["data"], 1)

	linked := decodeResult(t, h.toolCreateIncidentPublication(ctx, org, map[string]any{
		propIncidentUID: env.incident.UID, propPageIdentifier: "second",
		propTitle: "Linked", propSeverity: "minor",
	}))
	linkedUID, ok := linked["uid"].(string)
	r.True(ok)

	dup := h.toolCreateIncidentPublication(ctx, org, map[string]any{
		propIncidentUID: env.incident.UID, propPageIdentifier: "second",
	})
	r.True(dup.IsError)

	del := decodeResult(t, h.toolDeleteIncidentPublication(ctx, org, map[string]any{
		propIncidentUID: env.incident.UID, propPublicationUID: linkedUID,
	}))
	r.Equal(true, del["unpublished"])
}

func TestIncidentPublicationToolsErrors(t *testing.T) {
	t.Parallel()
	env := newCoverEnv(t)
	h := env.handler
	org := env.org.Slug

	tests := []struct {
		name string
		tool toolFunc
		args map[string]any
	}{
		{"list missing page arg", h.toolListStatusPageIncidents, map[string]any{}},
		{"list unknown page", h.toolListStatusPageIncidents, map[string]any{propPageIdentifier: "nope"}},
		{"create missing args", h.toolCreateStatusPageIncident, map[string]any{}},
		{"create unknown page", h.toolCreateStatusPageIncident,
			map[string]any{propPageIdentifier: "nope", propTitle: "t"}},
		{"create with unknown incident", h.toolCreateStatusPageIncident,
			map[string]any{propPageIdentifier: "nope", propTitle: "t", propIncidentUID: "x"}},
		{"update missing args", h.toolUpdateStatusPageIncident, map[string]any{}},
		{"update unknown page", h.toolUpdateStatusPageIncident,
			map[string]any{propPageIdentifier: "nope", propPublicationUID: "u"}},
		{"update with severity unknown page", h.toolUpdateStatusPageIncident,
			map[string]any{propPageIdentifier: "nope", propPublicationUID: "u", propSeverity: ""}},
		{"append missing args", h.toolCreateStatusPageIncidentUpdate, map[string]any{}},
		{"append unknown page", h.toolCreateStatusPageIncidentUpdate,
			map[string]any{propPageIdentifier: "nope", propPublicationUID: "u", propKind: "info", propBodyMarkdown: "b"}},
		{"publish missing args", h.toolCreateIncidentPublication, map[string]any{}},
		{"publish unknown incident", h.toolCreateIncidentPublication,
			map[string]any{propIncidentUID: "nope", propPageIdentifier: "p"}},
		{"unpublish missing args", h.toolDeleteIncidentPublication, map[string]any{}},
		{"unpublish unknown incident", h.toolDeleteIncidentPublication,
			map[string]any{propIncidentUID: "nope", propPublicationUID: "u"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			res := tc.tool(context.Background(), org, tc.args)
			r.True(res.IsError)
			r.NotEmpty(res.Content)
		})
	}
}
