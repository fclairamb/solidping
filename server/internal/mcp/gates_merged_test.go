package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// Gate tests for the merged tool surface (spec 2026-10-12-06): the scope,
// role and demo gates must refuse every manage_* call and every legacy alias
// of a write tool, whatever toolsets are listed.

// manageActions maps each manage_* tool to the actions it dispatches.
func manageActions() map[string][]string {
	return map[string][]string{
		toolManageStatusPageSection:  {actionCreate, actionUpdate, actionDelete},
		toolManageStatusPageResource: {actionCreate, actionUpdate, actionDelete},
		toolManageStatusPageIncident: {actionCreate, actionUpdate, actionAddUpdate, actionPublish, actionUnpublish},
	}
}

// expectedWrite is the naming rule the table must agree with.
func expectedWrite(name string) bool {
	for _, prefix := range []string{"create_", "update_", "delete_", "set_", "manage_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return name == toolRunCheck
}

func TestToolMutatesOverEveryRegisteredTool(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	r.Len(handler.toolMap, 50)

	for name := range handler.toolMap {
		r.Equalf(expectedWrite(name), toolMutates(name, map[string]any{}), "tool %q", name)
	}

	for tool, actions := range manageActions() {
		for _, action := range actions {
			r.Truef(toolMutates(tool, map[string]any{propAction: action}), "%s action=%s", tool, action)
		}

		for _, bad := range []any{nil, 3, "", "Delete", "frobnicate", []any{"create"}} {
			r.Truef(toolMutates(tool, map[string]any{propAction: bad}), "%s action=%v", tool, bad)
		}

		r.True(toolMutates(tool, nil), tool)
	}
}

// legacyWriteAliases are the legacy names that write.
func legacyWriteAliases() []string {
	var out []string

	for _, name := range legacyToolNames() {
		if expectedWrite(name) {
			out = append(out, name)
		}
	}

	return out
}

func rpcToolCall(t *testing.T, handler *Handler, claims *auth.Claims, tool string, args map[string]any) Response {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": methodToolsCall,
		"params": map[string]any{schemaKeyName: tool, "arguments": args},
	})
	require.NoError(t, err)

	rec, req := makeRequest(t, http.MethodPost, string(body), claims)
	require.NoError(t, handler.Handle(rec, req))

	return decodeResponse(t, rec)
}

// TestReadTokenRefusedOnManageAndLegacyWrites: an mcp:read token is refused on
// every manage_* action and every legacy write alias. The handler has no
// services, so a call that slipped past the gate would panic, not pass.
func TestReadTokenRefusedOnManageAndLegacyWrites(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	readOnly := claimsWithScopes(scopeMCPRead)

	refuse := func(tool string, args map[string]any) {
		resp := rpcToolCall(t, handler, readOnly, tool, args)
		r.NotNilf(resp.Error, "%s %v must be refused", tool, args)
		r.Equal(CodeForbidden, resp.Error.Code)
		r.Contains(resp.Error.Message, "mcp:read")
	}

	for tool, actions := range manageActions() {
		for _, action := range actions {
			refuse(tool, map[string]any{propAction: action})
		}

		refuse(tool, map[string]any{})
		refuse(tool, map[string]any{propAction: "Delete"})
	}

	aliases := legacyWriteAliases()
	r.Len(aliases, 11)

	for _, alias := range aliases {
		refuse(alias, map[string]any{})
	}

	// Positive control: the same token reaches a read merge.
	resp := rpcToolCall(t, handler, readOnly, toolProbe, map[string]any{propKind: "nope"})
	r.Nil(resp.Error)
}

// TestViewerRefusedOnManageAndLegacyWrites: the role gate takes the same
// decision for a full-scope token belonging to a viewer.
func TestViewerRefusedOnManageAndLegacyWrites(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newWriteRoleEnv(t)
	viewer := env.claims(env.users[models.MemberRoleViewer], scopeMCP)

	refuse := func(tool string, args map[string]any) {
		resp := rpcToolCall(t, env.handler, viewer, tool, args)
		r.NotNilf(resp.Error, "%s %v must be refused to a viewer", tool, args)
		r.Equal(CodeForbidden, resp.Error.Code)
		r.Equal("Tool "+tool+" requires the user role in this organization", resp.Error.Message)
	}

	for tool, actions := range manageActions() {
		for _, action := range actions {
			refuse(tool, map[string]any{propAction: action})
		}
	}

	for _, alias := range legacyWriteAliases() {
		refuse(alias, map[string]any{})
	}

	// Positive control: a user passes the gate and reaches the tool, which
	// answers with a tool error about its own arguments.
	user := env.claims(env.users[models.MemberRoleUser], scopeMCP)
	resp := rpcToolCall(t, env.handler, user, toolManageStatusPageSection, map[string]any{propAction: "nope"})
	r.Nil(resp.Error)
	r.Contains(toolResult(t, resp).Content[0].Text, "unknown action")
}

// TestDemoRefusedOnManageAndLegacyWrites: the demo session may only write its
// own checks; every manage_* action and every legacy write alias is refused.
func TestDemoRefusedOnManageAndLegacyWrites(t *testing.T) {
	t.Parallel()

	env := newDemoMCPEnv(t)
	claims := env.demoClaims()

	for tool, actions := range manageActions() {
		for _, action := range actions {
			requireDemoRefusal(t, env.tool(t, claims, tool, map[string]any{propAction: action}))
		}
	}

	for _, alias := range legacyWriteAliases() {
		requireDemoRefusal(t, env.tool(t, claims, alias, map[string]any{}))
	}

	require.True(t, demoToolRefused(claims, toolDeleteStatusPageSection, nil))
	require.False(t, demoToolRefused(claims, toolProbe, map[string]any{propKind: probeKindFetch}))
	require.False(t, demoToolRefused(claims, toolDeleteCheck, nil), "own checks stay writable")
}
