package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// demoMCPEnv is a REAL MCP handler (real NewHandler, real checks.Service over
// in-memory SQLite) with one org, one demo user and one seeded check whose
// created_by is NULL — the shape of the public demo catalog.
//
// The check tools are deliberately not stubbed: the point is that ownership is
// enforced by checks.Service exactly as on REST, which is only observable if
// the claims RequireMCPAuth put on the context really reach the service.
type demoMCPEnv struct {
	handler *Handler
	org     *models.Organization
	demo    *models.User
	seeded  *models.Check
	db      *sqlite.Service
}

func newDemoMCPEnv(t *testing.T) *demoMCPEnv {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)

	org := models.NewOrganization("mcpdemo", "MCP Demo")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	demo := models.NewUser("demo@mcpdemo.example")
	demo.Demo = true
	r.NoError(dbSvc.CreateUser(ctx, demo))
	// The demo principal is a `user` member, as job_startup_demo seeds it, so
	// the role gate admits it and only the demo gate is under test.
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, demo.UID, models.MemberRoleUser)))

	handler := NewHandler(dbSvc, notifier.NewLocalEventNotifier(), nil, nil, creds, nil, nil, nil)

	// Seeded with no claims on the context: created_by = NULL, like the
	// catalog the demo startup job writes.
	seeded, err := handler.checksSvc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:   "Seeded",
		Slug:   "seeded",
		Type:   "http",
		Config: map[string]any{"url": "https://acme.com"},
	})
	r.NoError(err)

	seededRow, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, seeded.UID)
	r.NoError(err)
	r.Nil(seededRow.CreatedBy, "fixture: a seeded check must carry created_by = NULL")

	return &demoMCPEnv{handler: handler, org: org, demo: demo, seeded: seededRow, db: dbSvc}
}

func (e *demoMCPEnv) demoClaims() *auth.Claims {
	return &auth.Claims{UserUID: e.demo.UID, OrgSlug: e.org.Slug, Demo: true}
}

// rpc drives one JSON-RPC request through the full Handle path.
func (e *demoMCPEnv) rpc(t *testing.T, claims *auth.Claims, method string, params any) Response {
	t.Helper()

	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		payload["params"] = params
	}

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	rec, req := makeRequest(t, http.MethodPost, string(body), claims)
	require.NoError(t, e.handler.Handle(rec, req))
	require.Equal(t, http.StatusOK, rec.Code)

	return decodeResponse(t, rec)
}

func (e *demoMCPEnv) tool(t *testing.T, claims *auth.Claims, name string, args map[string]any) Response {
	t.Helper()

	return e.rpc(t, claims, methodToolsCall, map[string]any{schemaKeyName: name, "arguments": args})
}

// toolResult decodes a successful tools/call result.
func toolResult(t *testing.T, resp Response) ToolCallResult {
	t.Helper()
	require.Nil(t, resp.Error, "expected a tool result, got a JSON-RPC error")

	raw, err := json.Marshal(resp.Result)
	require.NoError(t, err)

	var out ToolCallResult
	require.NoError(t, json.Unmarshal(raw, &out))

	return out
}

// requireDemoRefusal asserts the JSON-RPC refusal reads like the REST one:
// CodeForbidden, auth.DemoWriteMessage, data.code DEMO_READ_ONLY.
func requireDemoRefusal(t *testing.T, resp Response) {
	t.Helper()
	require.NotNil(t, resp.Error, "a demo session must be refused")
	require.Equal(t, CodeForbidden, resp.Error.Code)
	require.Equal(t, auth.DemoWriteMessage, resp.Error.Message)

	data, ok := resp.Error.Data.(map[string]any)
	require.True(t, ok, "the refusal must carry a data object")
	require.Equal(t, string(base.ErrorCodeDemoReadOnly), data["code"])
}

// TestMCPDemoSessionCanReadAndHandshake is the headline fix: a demo session
// used to get 403 DEMO_READ_ONLY on every POST, the handshake included.
func TestMCPDemoSessionCanReadAndHandshake(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newDemoMCPEnv(t)
	claims := env.demoClaims()

	initResp := env.rpc(t, claims, methodInitialize, map[string]any{"protocolVersion": protocolVersion2025_03_26})
	r.Nil(initResp.Error)

	tools := env.rpc(t, claims, methodToolsList, nil)
	r.Nil(tools.Error)
	r.Contains(fmt.Sprint(tools.Result), toolCreateCheck)

	list := toolResult(t, env.tool(t, claims, toolListChecks, map[string]any{}))
	r.False(list.IsError, "list_checks must work for a demo session")
	r.Contains(list.Content[0].Text, "seeded")
}

// TestMCPDemoSessionOwnsWhatItCreates proves the claims reach checks.Service:
// create_check stamps created_by with the demo user, and the demo session may
// then edit and delete that check — but never the seeded one.
func TestMCPDemoSessionOwnsWhatItCreates(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	env := newDemoMCPEnv(t)
	claims := env.demoClaims()

	created := toolResult(t, env.tool(t, claims, toolCreateCheck, map[string]any{
		schemaKeyName: "Mine",
		schemaKeySlug: "mine",
		schemaKeyType: "http",
		"config":      map[string]any{"url": "https://acme.com/health"},
	}))
	r.Falsef(created.IsError, "create_check must be allowed for a demo session: %v", created.Content)

	row, err := env.db.GetCheckByUidOrSlug(ctx, env.org.UID, "mine")
	r.NoError(err)
	r.NotNil(row.CreatedBy, "create_check must record the demo user as creator")
	r.Equal(env.demo.UID, *row.CreatedBy)

	// Positive control for the refusal below: the same session, the same tool,
	// on a check it owns, goes through.
	updated := toolResult(t, env.tool(t, claims, toolUpdateCheck, map[string]any{
		propIdentifier: "mine", schemaKeyName: "Mine renamed",
	}))
	r.Falsef(updated.IsError, "a demo session must be able to edit its own check: %v", updated.Content)

	// The seeded check (created_by NULL) is refused by checks.Service.
	refused := toolResult(t, env.tool(t, claims, toolUpdateCheck, map[string]any{
		propIdentifier: env.seeded.Slug, schemaKeyName: "Hijacked",
	}))
	r.True(refused.IsError, "update_check on a seeded check must be refused")
	r.Equal(auth.DemoWriteMessage, refused.Content[0].Text)

	deleted := toolResult(t, env.tool(t, claims, toolDeleteCheck, map[string]any{propIdentifier: env.seeded.Slug}))
	r.True(deleted.IsError, "delete_check on a seeded check must be refused")
	r.Equal(auth.DemoWriteMessage, deleted.Content[0].Text)

	seededAfter, err := env.db.GetCheckByUidOrSlug(ctx, env.org.UID, env.seeded.UID)
	r.NoError(err)
	r.NotNil(seededAfter.Name)
	r.Equal("Seeded", *seededAfter.Name, "the seeded check must be untouched")

	gone := toolResult(t, env.tool(t, claims, toolDeleteCheck, map[string]any{propIdentifier: "mine"}))
	r.Falsef(gone.IsError, "a demo session must be able to delete its own check: %v", gone.Content)
}

// TestMCPDemoSessionRefusedOnNonCheckMutations covers the rest of the mutation
// surface: every mutation tool outside the three check tools is refused before
// it runs, with the REST wording and code.
func TestMCPDemoSessionRefusedOnNonCheckMutations(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newDemoMCPEnv(t)
	claims := env.demoClaims()

	requireDemoRefusal(t, env.tool(t, claims, "create_status_page", map[string]any{
		schemaKeyName: "Demo page", schemaKeySlug: "demo-page",
	}))

	pages, err := env.handler.statusPagesSvc.ListStatusPages(t.Context(), env.org.Slug)
	r.NoError(err)
	r.Empty(pages, "the refused create_status_page must not have run")

	refused := 0

	for _, def := range env.handler.tools {
		if !isMutationTool(def.Name) {
			continue
		}

		resp := env.tool(t, claims, def.Name, map[string]any{})

		if _, allowed := demoAllowedMutationTools[def.Name]; allowed {
			if resp.Error != nil {
				r.NotEqualf(auth.DemoWriteMessage, resp.Error.Message,
					"%s is demo-allowed but the demo gate refused it", def.Name)
			}

			continue
		}

		requireDemoRefusal(t, resp)

		refused++
	}

	r.Greater(refused, 10, "the refused mutation tool set looks implausibly small")
}

// TestMCPDemoGateLeavesOtherSessionsAlone pins the first rule: a session that
// is not a demo session never sees the demo refusal.
func TestMCPDemoGateLeavesOtherSessionsAlone(t *testing.T) {
	t.Parallel()

	require.False(t, demoToolRefused(&auth.Claims{UserUID: "u"}, "create_status_page"))
	require.False(t, demoToolRefused(nil, "create_status_page"))
	require.True(t, demoToolRefused(&auth.Claims{UserUID: "u", Demo: true}, "create_status_page"))
	require.True(t, demoToolRefused(&auth.Claims{UserUID: "u", Demo: true}, toolSetMaintenanceWindowCheck))
	require.False(t, demoToolRefused(&auth.Claims{UserUID: "u", Demo: true}, toolDiagnoseCheck))
	require.False(t, demoToolRefused(&auth.Claims{UserUID: "u", Demo: true}, toolValidateCheck))
	require.False(t, demoToolRefused(&auth.Claims{UserUID: "u", Demo: true}, toolCreateCheck))
}
