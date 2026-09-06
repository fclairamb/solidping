package mcp

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// writeRoleEnv is one org with one member per role, plus a super admin and a
// stranger, wired into a Handler whose mutation tools are stubbed.
//
// The tools are stubbed on purpose: the assertion here is about the GATE, not
// about what create_check does. A stub also gives the positive control real
// teeth — a `user` must reach the tool, and "reached the tool" is only
// observable if the tool answers instead of panicking on a nil service.
type writeRoleEnv struct {
	handler *Handler
	org     *models.Organization
	users   map[models.MemberRole]*models.User
	super   *models.User
	orphan  *models.User
}

const writeRoleStubReply = "stub tool ran"

func newWriteRoleEnv(t *testing.T) *writeRoleEnv {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("mcprole", "MCP Role Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	env := &writeRoleEnv{org: org, users: map[models.MemberRole]*models.User{}}

	mkUser := func(email string, super bool) *models.User {
		user := models.NewUser(email)
		user.SuperAdmin = super
		r.NoError(dbSvc.CreateUser(ctx, user))

		return user
	}

	for _, role := range []models.MemberRole{
		models.MemberRoleOwner, models.MemberRoleAdmin,
		models.MemberRoleUser, models.MemberRoleViewer,
	} {
		user := mkUser(string(role)+"@mcprole.example", false)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, models.NewOrganizationMember(org.UID, user.UID, role)))
		env.users[role] = user
	}

	// A super admin holds no membership at all — that is the point.
	env.super = mkUser("super@mcprole.example", true)
	// A stranger is a real user with no membership in this org.
	env.orphan = mkUser("stranger@mcprole.example", false)

	env.handler = newWriteRoleHandler(t, dbSvc)

	return env
}

func newWriteRoleHandler(t *testing.T, dbSvc db.Service) *Handler {
	t.Helper()

	handler := &Handler{dbService: dbSvc}
	handler.registerTools()

	stub := func(_ context.Context, _ string, _ map[string]any) ToolCallResult {
		return ToolCallResult{Content: []ContentBlock{{Type: "text", Text: writeRoleStubReply}}}
	}

	// Stub both a mutating and a read-only tool so the pair below differs only
	// in the name's mutation prefix.
	handler.toolMap["create_check"] = stub
	handler.toolMap["list_checks"] = stub

	return handler
}

func (e *writeRoleEnv) claims(user *models.User, scopes ...string) *auth.Claims {
	return &auth.Claims{UserUID: user.UID, OrgSlug: e.org.Slug, Scopes: scopes}
}

// call drives one tools/call through the full Handle path.
func (e *writeRoleEnv) call(t *testing.T, tool string, claims *auth.Claims) Response {
	t.Helper()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":{}}}`
	rec, req := makeRequest(t, http.MethodPost, body, claims)
	require.NoError(t, e.handler.Handle(rec, req))
	require.Equal(t, http.StatusOK, rec.Code)

	return decodeResponse(t, rec)
}

// TestMCPMutationToolsRefuseViewers is §3's core assertion: an `mcp`-scoped PAT
// belonging to a viewer reaches the services directly, bypassing every REST
// middleware, so the write floor has to be repeated here or closing REST would
// be a half-fix that reads as a full one.
func TestMCPMutationToolsRefuseViewers(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newWriteRoleEnv(t)
	viewer := env.claims(env.users[models.MemberRoleViewer], scopeMCP)

	// Positive control: the very same credential works on a read tool. Without
	// it, a credential that was simply broken would produce the refusal below
	// and prove nothing about the role gate.
	read := env.call(t, "list_checks", viewer)
	r.Nil(read.Error, "a viewer must still be able to read through MCP")

	// The gate.
	write := env.call(t, "create_check", viewer)
	r.NotNil(write.Error)
	r.Equal(CodeForbidden, write.Error.Code)
	r.Equal("Tool create_check requires the user role in this organization", write.Error.Message)
	r.NotContains(write.Error.Message, "mcp:read",
		"the ROLE denial must not be confused with the SCOPE denial")
}

// TestMCPMutationToolsAllowUsers is the other half: the gate must not lock out
// the roles it is meant to admit. A floor that also refuses `user` would be
// worse than the hole it closes.
func TestMCPMutationToolsAllowUsers(t *testing.T) {
	t.Parallel()

	env := newWriteRoleEnv(t)

	for _, role := range []models.MemberRole{
		models.MemberRoleUser, models.MemberRoleAdmin, models.MemberRoleOwner,
	} {
		t.Run(string(role), func(t *testing.T) {
			t.Parallel()

			resp := env.call(t, "create_check", env.claims(env.users[role], scopeMCP))
			require.Nilf(t, resp.Error, "%s must be allowed to mutate through MCP", role)
			require.Contains(t, fmt.Sprint(resp.Result), writeRoleStubReply,
				"the tool itself must have run, not merely the gate")
		})
	}
}

// TestMCPMutationRoleGateFailsClosed pins the refusals that are not about the
// role at all: an unknown user, a user with no membership in this org, and a
// handler with no database. Each is a state the gate cannot reason about, and
// each must deny rather than fall through.
func TestMCPMutationRoleGateFailsClosed(t *testing.T) {
	t.Parallel()

	env := newWriteRoleEnv(t)

	t.Run("non-member", func(t *testing.T) {
		t.Parallel()

		resp := env.call(t, "create_check", env.claims(env.orphan, scopeMCP))
		require.NotNil(t, resp.Error)
		require.Equal(t, CodeForbidden, resp.Error.Code)
	})

	t.Run("unknown user", func(t *testing.T) {
		t.Parallel()

		resp := env.call(t, "create_check",
			&auth.Claims{UserUID: "no-such-user", OrgSlug: env.org.Slug, Scopes: []string{scopeMCP}})
		require.NotNil(t, resp.Error)
		require.Equal(t, CodeForbidden, resp.Error.Code)
	})

	t.Run("unknown org", func(t *testing.T) {
		t.Parallel()

		claims := env.claims(env.users[models.MemberRoleOwner], scopeMCP)
		claims.OrgSlug = "no-such-org"

		resp := env.call(t, "create_check", claims)
		require.NotNil(t, resp.Error)
		require.Equal(t, CodeForbidden, resp.Error.Code)
	})

	t.Run("no database", func(t *testing.T) {
		t.Parallel()

		require.NotEmpty(t, (&Handler{}).mutationRoleDenial(
			t.Context(), "any", "create_check", &auth.Claims{UserUID: "u"}))
	})

	t.Run("nil claims", func(t *testing.T) {
		t.Parallel()

		require.NotEmpty(t, env.handler.mutationRoleDenial(t.Context(), env.org.Slug, "create_check", nil))
	})
}

// TestMCPMutationRoleGateAllowsSuperAdmins mirrors requireOrgRole: a super
// admin holds no membership row anywhere, so a gate that only read memberships
// would lock them out of every org.
func TestMCPMutationRoleGateAllowsSuperAdmins(t *testing.T) {
	t.Parallel()

	env := newWriteRoleEnv(t)

	resp := env.call(t, "create_check", env.claims(env.super, scopeMCP))
	require.Nil(t, resp.Error)
}

// TestMCPScopeDenialStillWinsForReadOnlyTokens pins the ordering: an mcp:read
// token held by an OWNER is still refused, and refused for the scope reason.
// The role gate must not have widened the scope gate on its way in.
func TestMCPScopeDenialStillWinsForReadOnlyTokens(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newWriteRoleEnv(t)

	resp := env.call(t, "create_check", env.claims(env.users[models.MemberRoleOwner], scopeMCPRead))
	r.NotNil(resp.Error)
	r.Equal(CodeForbidden, resp.Error.Code)
	r.Contains(resp.Error.Message, "mcp:read")
}

// TestMCPMutationRoleGateReadsTheMembershipRow proves the "membership row, not
// claims" decision: a token minted while its owner was a `user` — and still
// carrying that role in its claims — stops writing the moment the membership
// is demoted, with no re-login and no token refresh.
func TestMCPMutationRoleGateReadsTheMembershipRow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newWriteRoleEnv(t)
	ctx := t.Context()

	user := env.users[models.MemberRoleUser]

	claims := env.claims(user, scopeMCP)
	claims.Role = string(models.MemberRoleUser)

	r.Nil(env.call(t, "create_check", claims).Error, "the fixture must start out allowed")

	member, err := env.handler.dbService.GetMemberByUserAndOrg(ctx, user.UID, env.org.UID)
	r.NoError(err)

	viewer := models.MemberRoleViewer
	r.NoError(env.handler.dbService.UpdateOrganizationMember(ctx, member.UID,
		models.OrganizationMemberUpdate{Role: &viewer}))

	resp := env.call(t, "create_check", claims)
	r.NotNil(resp.Error, "the same token, unchanged, must stop writing after the demotion")
	r.Equal(CodeForbidden, resp.Error.Code)
}

// TestIsMutationToolCoversEveryRegisteredWriteTool guards the reuse decision in
// §3: the role gate keys off isMutationTool's prefix list rather than a second
// table, so that list has to actually describe the tool set. Every registered
// tool whose name starts with a mutation verb must be recognized, and no
// obviously-read tool may be.
func TestIsMutationToolCoversEveryRegisteredWriteTool(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	r.NotEmpty(handler.tools)

	mutating := 0

	for _, tool := range handler.tools {
		switch {
		case isMutationTool(tool.Name):
			mutating++
		default:
			r.NotContainsf([]string{"create", "update", "delete", "set"},
				firstWord(tool.Name), "tool %q looks mutating but is not gated", tool.Name)
		}
	}

	r.Greater(mutating, 10, "the mutation tool set looks implausibly small")
}

func firstWord(name string) string {
	for i := range len(name) {
		if name[i] == '_' {
			return name[:i]
		}
	}

	return name
}
