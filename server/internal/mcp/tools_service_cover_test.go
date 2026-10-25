package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

func newServiceEnv(t *testing.T) (*Handler, *models.Organization, *models.Incident, string) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)

	org := models.NewOrganization("svcorg", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	h := NewHandler(dbSvc, notifier.NewLocalEventNotifier(), nil, nil, creds, nil, nil, nil)

	created := h.toolCreateCheck(ctx, org.Slug, map[string]any{
		"name": "Acme API", "slug": "acme-api", "type": "http",
		"config": map[string]any{"url": "https://acme.com"}, "period": "00:01:00",
		"labels": map[string]any{"env": "prod"}, "confirmationPeriodSeconds": float64(0),
		"recoveryPeriodSeconds": float64(0),
	})
	r.False(created.IsError, "%v", created.Content)

	var check struct {
		UID string `json:"uid"`
	}
	r.NoError(json.Unmarshal([]byte(created.Content[0].Text), &check))

	incident := models.NewIncident(org.UID, check.UID, time.Now(), "acme api down")
	r.NoError(dbSvc.CreateIncident(ctx, incident))

	return h, org, incident, check.UID
}

func TestChecksToolsAgainstServices(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	h, org, _, checkUID := newServiceEnv(t)
	slug := org.Slug

	ok := func(res ToolCallResult) {
		t.Helper()
		r.False(res.IsError, "%v", res.Content)
	}
	bad := func(res ToolCallResult) {
		t.Helper()
		r.True(res.IsError, "%v", res.Content)
	}

	ok(h.toolListChecks(ctx, slug, map[string]any{
		"limit": float64(0), "labels": map[string]any{"env": "prod"}, "with": "lastResult,lastStatusChange",
	}))
	ok(h.toolListChecks(ctx, slug, map[string]any{"limit": float64(5000), "checkGroupUid": "nope-group"}))

	bad(h.toolGetCheck(ctx, slug, map[string]any{}))
	bad(h.toolGetCheck(ctx, slug, map[string]any{propIdentifier: "missing"}))
	ok(h.toolGetCheck(ctx, slug, map[string]any{propIdentifier: checkUID, "with": "lastResult,lastStatusChange"}))

	ok(h.toolUpdateCheck(ctx, slug, map[string]any{
		propIdentifier: "acme-api", "slug": "acme-api", "description": "d", "name": "Acme API 2",
		"config": map[string]any{"url": "https://acme.org"}, "regions": []any{}, "regionPool": []any{},
		"period": "00:02:00", "labels": map[string]any{"env": "staging"}, "checkGroupUid": "",
		"confirmationPeriodSeconds": float64(30), "recoveryPeriodSeconds": float64(30),
	}))
	bad(h.toolUpdateCheck(ctx, slug, map[string]any{propIdentifier: "missing", "name": "x"}))

	ok(h.toolDiagnoseCheck(ctx, slug, map[string]any{propIdentifier: checkUID, propRecentResultsLimit: float64(3)}))
	bad(h.toolDiagnoseCheck(ctx, slug, map[string]any{}))
	bad(h.toolDiagnoseCheck(ctx, slug, map[string]any{propIdentifier: "missing"}))
}

func TestIncidentAndMaintenanceToolsAgainstServices(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	h, org, incident, checkUID := newServiceEnv(t)
	slug := org.Slug

	ok := func(res ToolCallResult) ToolCallResult {
		t.Helper()
		r.False(res.IsError, "%v", res.Content)

		return res
	}
	bad := func(res ToolCallResult) {
		t.Helper()
		r.True(res.IsError, "%v", res.Content)
	}

	ok(h.toolListIncidents(ctx, slug, map[string]any{
		"size": float64(0), "order": "asc", propCheckUID: checkUID, propState: "active",
		"since": "2020-01-01T00:00:00Z", "until": "2099-01-01T00:00:00Z", "with": "check",
	}))
	ok(h.toolListIncidents(ctx, slug, map[string]any{"size": float64(1000), "order": "desc", "since": "garbage"}))
	bad(h.toolListIncidents(ctx, slug, map[string]any{"order": "sideways"}))

	bad(h.toolGetIncident(ctx, slug, map[string]any{}))
	ok(h.toolGetIncident(ctx, slug, map[string]any{propUID: incident.UID}))
	bad(h.toolGetIncident(ctx, slug, map[string]any{propUID: "missing"}))

	bad(h.toolIncidentNotificationsList(ctx, slug, map[string]any{}))
	ok(h.toolIncidentNotificationsList(ctx, slug, map[string]any{propUID: incident.UID, "limit": float64(0)}))
	ok(h.toolIncidentNotificationsList(ctx, slug,
		map[string]any{propUID: incident.UID, "limit": float64(9999), propStatus: "sent"}))
	bad(h.toolIncidentNotificationsList(ctx, "nope", map[string]any{propUID: incident.UID}))

	// Maintenance windows.
	bad(h.toolCreateMaintenanceWindow(ctx, slug, map[string]any{}))
	created := ok(h.toolCreateMaintenanceWindow(ctx, slug, map[string]any{
		propTitle: "Upgrade", propStartAt: "2099-01-01T00:00:00Z", propEndAt: "2099-01-01T02:00:00Z",
		"description": "d", propCheckUIDs: []any{checkUID},
	}))

	var window struct {
		UID string `json:"uid"`
	}
	r.NoError(json.Unmarshal([]byte(created.Content[0].Text), &window))
	r.NotEmpty(window.UID)

	ok(h.toolListMaintenanceWindows(ctx, slug, map[string]any{propLimit: float64(0), propStatus: "scheduled"}))
	ok(h.toolListMaintenanceWindows(ctx, slug, nil))
	bad(h.toolGetMaintenanceWindow(ctx, slug, map[string]any{}))
	ok(h.toolGetMaintenanceWindow(ctx, slug, map[string]any{propUID: window.UID}))
	bad(h.toolGetMaintenanceWindow(ctx, slug, map[string]any{propUID: "missing"}))
	ok(h.toolUpdateMaintenanceWindow(ctx, slug, map[string]any{
		propUID: window.UID, propTitle: "Upgrade 2", "description": "d2",
		propStartAt: "2099-01-02T00:00:00Z", propEndAt: "2099-01-02T02:00:00Z", propRecurrence: "none",
	}))
	bad(h.toolUpdateMaintenanceWindow(ctx, slug, map[string]any{}))
	bad(h.toolUpdateMaintenanceWindow(ctx, slug, map[string]any{propUID: window.UID, propStartAt: "garbage"}))
	ok(h.toolSetMaintenanceWindowChecks(ctx, slug, map[string]any{propUID: window.UID, propCheckUIDs: []any{checkUID}}))
	bad(h.toolSetMaintenanceWindowChecks(ctx, slug, map[string]any{}))
	ok(h.toolDeleteMaintenanceWindow(ctx, slug, map[string]any{propUID: window.UID}))
	bad(h.toolDeleteMaintenanceWindow(ctx, slug, map[string]any{}))
}

func TestMiscToolsAgainstServices(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	h, org, _, _ := newServiceEnv(t)
	slug := org.Slug

	r.False(h.toolListIntegrations(ctx, slug, map[string]any{schemaKeyType: "webhook"}).IsError)
	r.False(h.toolListIntegrations(ctx, slug, nil).IsError)
	r.True(h.toolCreateIntegration(ctx, slug, map[string]any{}).IsError)
	r.False(h.toolListCheckGroups(ctx, slug, nil).IsError)
	r.False(h.toolListRegions(ctx, slug, nil).IsError)

	for _, uri := range []string{uriOrganization, uriRegions, "solidping://unknown"} {
		raw, err := json.Marshal(ResourceReadParams{URI: uri})
		r.NoError(err)

		resp, status := h.handleResourcesRead(ctx, &Request{ID: json.RawMessage("1"), Params: raw}, slug)
		r.Equal(http.StatusOK, status)
		r.Equal(uri == "solidping://unknown", resp.Error != nil, uri)
	}

	resp, _ := h.handleResourcesRead(ctx, &Request{ID: json.RawMessage("1"), Params: json.RawMessage(`[`)}, slug)
	r.NotNil(resp.Error)

	resp, _ = h.handleResourcesRead(context.Background(), &Request{
		ID: json.RawMessage("1"), Params: json.RawMessage(`{"uri":"` + uriOrganization + `"}`),
	}, "nope")
	r.NotNil(resp.Error)
}
