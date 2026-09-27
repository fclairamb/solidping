package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// stdioEnv is a REAL MCP handler over a real database, with one org holding
// two owners (the older one is the default principal), a viewer and a demo
// user, plus an org with no owner and a stranger who belongs to nothing.
//
// The first owner carries must_change_password, the shape of the admin a
// fresh database seeds: the stdio session must act as it all the same.
//
// Two more orgs exercise the owner choice: acme-tie has two owners joined at
// the same instant, and acme-gone has an older owner whose user is
// soft-deleted and an older owner membership that is soft-deleted, ahead of
// the one live owner.
type stdioEnv struct {
	handler   *Handler
	db        db.Service
	org       *models.Organization
	ownerless *models.Organization
	owner     *models.User
	owner2    *models.User
	viewer    *models.User
	demo      *models.User
	stranger  *models.User
	super     *models.User
	// tieWinner is the acme-tie owner whose membership uid sorts first.
	tieWinner *models.User
	// goneLive is the only acme-gone owner still standing.
	goneLive *models.User
}

// newStdioEnv builds the env over in-memory SQLite.
func newStdioEnv(t *testing.T) *stdioEnv {
	t.Helper()
	r := require.New(t)

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return newStdioEnvOn(t, dbSvc)
}

// newStdioEnvOn seeds the env on an initialized database of either engine.
//
//nolint:funlen // one fixture, several orgs, each line is a row
func newStdioEnvOn(t *testing.T, dbSvc db.Service) *stdioEnv {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)

	env := &stdioEnv{db: dbSvc}

	env.org = models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, env.org))

	env.ownerless = models.NewOrganization("acme-empty", "Acme Empty")
	r.NoError(dbSvc.CreateOrganization(ctx, env.ownerless))

	mkUser := func(email string, mutate func(*models.User)) *models.User {
		user := models.NewUser(email)
		if mutate != nil {
			mutate(user)
		}

		r.NoError(dbSvc.CreateUser(ctx, user))

		return user
	}

	// Whole seconds, so a created_at survives either engine's precision and
	// two memberships given the same instant really compare equal.
	now := time.Now().Truncate(time.Second)

	join := func(
		org *models.Organization, user *models.User, role models.MemberRole, age time.Duration,
	) *models.OrganizationMember {
		member := models.NewOrganizationMember(org.UID, user.UID, role)
		member.CreatedAt = now.Add(-age)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

		return member
	}

	env.owner = mkUser("alice@acme.com", func(u *models.User) { u.MustChangePassword = true })
	env.owner2 = mkUser("bob@acme.com", nil)
	env.viewer = mkUser("carol@acme.com", nil)
	env.demo = mkUser("demo@acme.com", func(u *models.User) { u.Demo = true })
	env.stranger = mkUser("dave@acme.com", nil)
	env.super = mkUser("root@acme.com", func(u *models.User) { u.SuperAdmin = true })

	// bob joined AFTER alice and is listed first by ListMembersByOrg
	// (created_at DESC): picking the oldest owner is a real choice here.
	join(env.org, env.owner, models.MemberRoleOwner, 2*time.Hour)
	join(env.org, env.owner2, models.MemberRoleOwner, time.Hour)
	join(env.org, env.viewer, models.MemberRoleViewer, time.Minute)
	join(env.org, env.demo, models.MemberRoleUser, time.Minute)
	join(env.org, env.super, models.MemberRoleAdmin, time.Minute)
	join(env.ownerless, env.viewer, models.MemberRoleAdmin, time.Minute)

	tieOrg := models.NewOrganization("acme-tie", "Acme Tie")
	r.NoError(dbSvc.CreateOrganization(ctx, tieOrg))

	tieA := mkUser("erin@acme.com", nil)
	tieB := mkUser("frank@acme.com", nil)
	memberA := join(tieOrg, tieA, models.MemberRoleOwner, time.Hour)
	memberB := join(tieOrg, tieB, models.MemberRoleOwner, time.Hour)

	env.tieWinner = tieA
	if memberB.UID < memberA.UID {
		env.tieWinner = tieB
	}

	goneOrg := models.NewOrganization("acme-gone", "Acme Gone")
	r.NoError(dbSvc.CreateOrganization(ctx, goneOrg))

	goneUser := mkUser("grace@acme.com", nil)
	goneMember := mkUser("heidi@acme.com", nil)
	env.goneLive = mkUser("ivan@acme.com", nil)

	join(goneOrg, goneUser, models.MemberRoleOwner, 3*time.Hour)
	leftMembership := join(goneOrg, goneMember, models.MemberRoleOwner, 2*time.Hour)
	join(goneOrg, env.goneLive, models.MemberRoleOwner, time.Hour)

	r.NoError(dbSvc.DeleteUser(ctx, goneUser.UID))
	r.NoError(dbSvc.DeleteOrganizationMember(ctx, leftMembership.UID))

	env.handler = NewHandler(dbSvc, notifier.NewLocalEventNotifier(), nil, nil, creds, nil, nil, nil)

	return env
}

func TestResolveStdioPrincipal(t *testing.T) {
	t.Parallel()

	runResolveStdioPrincipalCases(t, newStdioEnv(t))
}

// runResolveStdioPrincipalCases is the principal table, shared by the SQLite
// test above and its Postgres twin (stdio_postgres_test.go).
//
//nolint:funlen // a table
func runResolveStdioPrincipalCases(t *testing.T, env *stdioEnv) {
	t.Helper()

	tests := []struct {
		name string
		org  string
		user string
		// userIsViewerUID passes the viewer's uid as --user (only known
		// once the env exists).
		userIsViewerUID bool
		wantUser        func(*stdioEnv) *models.User
		wantRole        models.MemberRole
		wantErr         error
	}{
		{
			name:     "default is the oldest owner",
			org:      "acme",
			wantUser: func(e *stdioEnv) *models.User { return e.owner },
			wantRole: models.MemberRoleOwner,
		},
		{
			name:     "user by email",
			org:      "acme",
			user:     "carol@acme.com",
			wantUser: func(e *stdioEnv) *models.User { return e.viewer },
			wantRole: models.MemberRoleViewer,
		},
		{
			name:     "user by email ignores case",
			org:      "acme",
			user:     "Bob@Acme.com",
			wantUser: func(e *stdioEnv) *models.User { return e.owner2 },
			wantRole: models.MemberRoleOwner,
		},
		{
			name:            "user by uid",
			org:             "acme",
			userIsViewerUID: true,
			wantUser:        func(e *stdioEnv) *models.User { return e.viewer },
			wantRole:        models.MemberRoleViewer,
		},
		{name: "unknown org", org: "nosuch", wantErr: ErrStdioOrgNotFound},
		{name: "unknown org with a user", org: "nosuch", user: "alice@acme.com", wantErr: ErrStdioOrgNotFound},
		{name: "unknown user email", org: "acme", user: "nobody@acme.com", wantErr: ErrStdioUserNotFound},
		{name: "neither email nor uid", org: "acme", user: "alice", wantErr: ErrStdioUserNotFound},
		{name: "malformed uid", org: "acme", user: "not-a-uid", wantErr: ErrStdioUserNotFound},
		{name: "braced uid", org: "acme", user: "{7c9e6679-7425-40de-944b-e07fc1f90ae7}", wantErr: ErrStdioUserNotFound},
		{name: "unknown uid", org: "acme", user: "7c9e6679-7425-40de-944b-e07fc1f90ae7", wantErr: ErrStdioUserNotFound},
		{name: "soft-deleted user", org: "acme-gone", user: "grace@acme.com", wantErr: ErrStdioUserNotFound},
		{name: "soft-deleted membership", org: "acme-gone", user: "heidi@acme.com", wantErr: ErrStdioNotMember},
		{
			name:     "owners tied on created_at: lowest membership uid",
			org:      "acme-tie",
			wantUser: func(e *stdioEnv) *models.User { return e.tieWinner },
			wantRole: models.MemberRoleOwner,
		},
		{
			name:     "default owner skips a soft-deleted user and membership",
			org:      "acme-gone",
			wantUser: func(e *stdioEnv) *models.User { return e.goneLive },
			wantRole: models.MemberRoleOwner,
		},
		{name: "not a member", org: "acme", user: "dave@acme.com", wantErr: ErrStdioNotMember},
		{name: "member of another org only", org: "acme-empty", user: "alice@acme.com", wantErr: ErrStdioNotMember},
		{name: "org without an owner", org: "acme-empty", wantErr: ErrStdioNoOwner},
		{
			name:     "org without an owner still takes --user",
			org:      "acme-empty",
			user:     "carol@acme.com",
			wantUser: func(e *stdioEnv) *models.User { return e.viewer },
			wantRole: models.MemberRoleAdmin,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			user := tc.user
			if tc.userIsViewerUID {
				user = env.viewer.UID
			}

			principal, err := ResolveStdioPrincipal(t.Context(), env.db, tc.org, user)
			if tc.wantErr != nil {
				r.ErrorIs(err, tc.wantErr)
				r.Nil(principal)

				return
			}

			r.NoError(err)
			r.Equal(tc.wantUser(env).UID, principal.User.UID)
			r.Equal(tc.org, principal.Org.Slug)
			r.Equal(tc.wantRole, principal.Role)
		})
	}
}

// TestPickStdioOwnerIgnoresListOrder: the owner choice depends on created_at
// and then on the membership uid, never on the order the database listed the
// memberships in.
func TestPickStdioOwnerIgnoresListOrder(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	live := func(uid string, role models.MemberRole, createdAt time.Time) *models.OrganizationMember {
		return &models.OrganizationMember{
			UID: uid, Role: role, CreatedAt: createdAt, User: &models.User{UID: "user-" + uid},
		}
	}

	deletedAt := at.Add(-time.Minute)
	goneUser := live("0-gone-user", models.MemberRoleOwner, at.Add(-time.Hour))
	goneUser.User.DeletedAt = &deletedAt
	goneMembership := live("0-gone-membership", models.MemberRoleOwner, at.Add(-time.Hour))
	goneMembership.DeletedAt = &deletedAt

	members := []*models.OrganizationMember{
		live("b", models.MemberRoleOwner, at),
		live("a", models.MemberRoleOwner, at),
		live("0-admin", models.MemberRoleAdmin, at.Add(-time.Hour)),
		live("c", models.MemberRoleOwner, at.Add(time.Second)),
		goneUser,
		goneMembership,
		{UID: "0-no-user", Role: models.MemberRoleOwner, CreatedAt: at.Add(-time.Hour)},
	}

	for range 2 {
		owner := pickStdioOwner(members)
		r.NotNil(owner)
		r.Equal("a", owner.UID)

		slices.Reverse(members)
	}

	r.Nil(pickStdioOwner(nil))
	r.Nil(pickStdioOwner([]*models.OrganizationMember{goneUser, goneMembership}))
}

// TestStdioPrincipalClaims pins the claims a stdio session carries: those of
// a full-scope MCP PAT of the same user, super admins included.
func TestStdioPrincipalClaims(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newStdioEnv(t)

	principal, err := ResolveStdioPrincipal(t.Context(), env.db, "acme", "")
	r.NoError(err)

	claims := principal.Claims()
	r.Equal(env.owner.UID, claims.UserUID)
	r.Equal("acme", claims.OrgSlug)
	r.Equal(string(models.MemberRoleOwner), claims.Role)
	r.Equal([]string{scopeMCP}, claims.Scopes)
	r.True(hasMCPAccess(claims))
	r.False(isMCPReadOnly(claims))
	r.False(claims.Demo)

	super, err := ResolveStdioPrincipal(t.Context(), env.db, "acme", "root@acme.com")
	r.NoError(err)
	r.Equal(auth.RoleSuperAdmin, super.Claims().Role)
}

// runStdio serves lines to the handler as principal and returns every stdout
// line, each decoded as a JSON object.
func runStdio(t *testing.T, env *stdioEnv, principal *StdioPrincipal, lines ...string) []map[string]json.RawMessage {
	t.Helper()
	r := require.New(t)

	var out bytes.Buffer

	input := strings.NewReader(strings.Join(lines, "\n") + "\n")
	r.NoError(env.handler.ServeStdio(t.Context(), input, &out, principal))

	return decodeStdioLines(t, out.String())
}

// decodeStdioLines checks that every line is one JSON-RPC 2.0 message and
// decodes it. A batch reply decodes as its members, in order.
func decodeStdioLines(t *testing.T, stdout string) []map[string]json.RawMessage {
	t.Helper()
	r := require.New(t)

	var messages []map[string]json.RawMessage

	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") {
			var batch []map[string]json.RawMessage
			r.NoError(json.Unmarshal([]byte(line), &batch), "batch line: %s", line)
			messages = append(messages, batch...)

			continue
		}

		var msg map[string]json.RawMessage
		r.NoError(json.Unmarshal([]byte(line), &msg), "stdout line is not a JSON object: %s", line)
		r.JSONEq(`"2.0"`, string(msg["jsonrpc"]), "line: %s", line)
		messages = append(messages, msg)
	}

	return messages
}

func stdioToolResult(t *testing.T, msg map[string]json.RawMessage) ToolCallResult {
	t.Helper()
	r := require.New(t)
	r.NotContains(msg, "error", "expected a result, got %s", msg["error"])

	var result ToolCallResult
	r.NoError(json.Unmarshal(msg["result"], &result))

	return result
}

func stdioError(t *testing.T, msg map[string]json.RawMessage) RPCError {
	t.Helper()
	r := require.New(t)
	r.Contains(msg, "error", "expected an error, got %s", msg["result"])

	var rpcErr RPCError
	r.NoError(json.Unmarshal(msg["error"], &rpcErr))

	return rpcErr
}

// TestServeStdioRoundTrip is the headline: initialize, tools/list and a
// create_check whose created_by is the principal — the must-change-password
// owner — with notifications answered by nothing at all.
func TestServeStdioRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newStdioEnv(t)

	principal, err := ResolveStdioPrincipal(t.Context(), env.db, "acme", "")
	r.NoError(err)
	r.True(principal.User.MustChangePassword, "fixture: the default principal must be flagged")

	msgs := runStdio(t, env, principal,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26",`+
			`"clientInfo":{"name":"acme-client","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"two","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_check","arguments":`+
			`{"name":"Acme","slug":"acme-web","type":"http","config":{"url":"https://acme.com"}}}}`,
	)
	r.Len(msgs, 3, "one reply per request, none for the notification")

	r.JSONEq(`1`, string(msgs[0]["id"]))

	var initResult InitializeResult
	r.NoError(json.Unmarshal(msgs[0]["result"], &initResult))
	r.Equal("solidping", initResult.ServerInfo.Name)
	r.Equal(protocolVersion2025_03_26, initResult.ProtocolVersion)
	r.NotNil(initResult.Capabilities.Tools)

	r.JSONEq(`"two"`, string(msgs[1]["id"]))

	var tools ToolsListResult
	r.NoError(json.Unmarshal(msgs[1]["result"], &tools))
	r.NotEmpty(tools.Tools)

	r.JSONEq(`3`, string(msgs[2]["id"]))
	result := stdioToolResult(t, msgs[2])
	r.False(result.IsError, "create_check failed: %v", result.Content)

	check, err := env.db.GetCheckByUidOrSlug(t.Context(), env.org.UID, "acme-web")
	r.NoError(err)
	r.NotNil(check.CreatedBy, "created_by must be stamped from the stdio principal")
	r.Equal(env.owner.UID, *check.CreatedBy)

	// The process is the session: stdio never grows the HTTP session map.
	count := 0

	env.handler.sessions.Range(func(_, _ any) bool {
		count++

		return true
	})
	r.Zero(count)
}

// TestServeStdioNotificationsGetNoOutput: a notification is never answered,
// not even when it fails (unknown method, a failing tool call, bad params).
func TestServeStdioNotificationsGetNoOutput(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newStdioEnv(t)

	principal, err := ResolveStdioPrincipal(t.Context(), env.db, "acme", "")
	r.NoError(err)

	var out bytes.Buffer

	input := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		//nolint:misspell // the MCP method is spelled this way
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		`{"jsonrpc":"2.0","id":null,"method":"no/such/method"}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"no_such_tool"}}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":"not an object"}`,
		`[{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","method":"ping"}]`,
		``,
		`   `,
	}, "\n"))

	r.NoError(env.handler.ServeStdio(t.Context(), input, &out, principal))
	r.Empty(out.String(), "notifications must produce no output at all")
}

// TestServeStdioProtocolEdges covers what the dispatcher never sees: framing
// errors, batches, id echoing and a request named like a notification.
func TestServeStdioProtocolEdges(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newStdioEnv(t)

	principal, err := ResolveStdioPrincipal(t.Context(), env.db, "acme", "")
	r.NoError(err)

	msgs := runStdio(t, env, principal,
		`{not json`,
		`[]`,
		`{"jsonrpc":"1.0","id":7,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":9007199254740993,"method":"ping"}`,
		`[{"jsonrpc":"2.0","id":"a","method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},42]`,
		`{"jsonrpc":"2.0","id":8,"method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":10,"method":"no/such/method"}`,
	)
	r.Len(msgs, 8)

	r.JSONEq(`null`, string(msgs[0]["id"]))
	r.Equal(CodeParseError, stdioError(t, msgs[0]).Code)

	r.JSONEq(`null`, string(msgs[1]["id"]))
	r.Equal(CodeInvalidRequest, stdioError(t, msgs[1]).Code)

	r.JSONEq(`7`, string(msgs[2]["id"]))
	r.Equal(CodeInvalidRequest, stdioError(t, msgs[2]).Code)

	// Echoed byte for byte: a float64 round trip would print ...992.
	r.Equal(`9007199254740993`, string(msgs[3]["id"]))
	r.JSONEq(`{}`, string(msgs[3]["result"]))

	// The batch: the ping's reply, then an Invalid Request for the 42, and
	// nothing for its notification.
	r.JSONEq(`"a"`, string(msgs[4]["id"]))
	r.JSONEq(`{}`, string(msgs[4]["result"]))
	r.JSONEq(`null`, string(msgs[5]["id"]))
	r.Equal(CodeInvalidRequest, stdioError(t, msgs[5]).Code)

	// With an id, it is a request and is owed an answer.
	r.JSONEq(`8`, string(msgs[6]["id"]))
	r.JSONEq(`{}`, string(msgs[6]["result"]))

	r.JSONEq(`10`, string(msgs[7]["id"]))
	r.Equal(CodeMethodNotFound, stdioError(t, msgs[7]).Code)
}

// TestServeStdioGatesStillRun: the role gate reads the principal's membership
// (a viewer cannot write), and a demo user stays a demo session, exactly as
// the claims RequireMCPAuth builds would make them.
func TestServeStdioGatesStillRun(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newStdioEnv(t)

	createCheck := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_check","arguments":` +
		`{"name":"Acme","slug":"acme-gate","type":"http","config":{"url":"https://acme.com"}}}}`
	listChecks := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_checks","arguments":{}}}`

	viewer, err := ResolveStdioPrincipal(t.Context(), env.db, "acme", "carol@acme.com")
	r.NoError(err)

	msgs := runStdio(t, env, viewer, createCheck, listChecks)
	r.Len(msgs, 2)
	r.Equal(CodeForbidden, stdioError(t, msgs[0]).Code, "a viewer must be refused a write")
	r.False(stdioToolResult(t, msgs[1]).IsError, "a viewer may still read")

	demo, err := ResolveStdioPrincipal(t.Context(), env.db, "acme", "demo@acme.com")
	r.NoError(err)

	msgs = runStdio(t, env, demo,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_maintenance_window","arguments":`+
			`{"title":"Acme","startsAt":"2030-01-01T00:00:00Z","endsAt":"2030-01-01T01:00:00Z"}}}`,
		createCheck,
	)
	r.Len(msgs, 2)

	refusal := stdioError(t, msgs[0])
	r.Equal(CodeForbidden, refusal.Code)
	r.Equal(auth.DemoWriteMessage, refusal.Message, "a demo user must stay a demo session over stdio")
	r.False(stdioToolResult(t, msgs[1]).IsError, "the demo may still create checks")

	check, err := env.db.GetCheckByUidOrSlug(t.Context(), env.org.UID, "acme-gate")
	r.NoError(err)
	r.NotNil(check.CreatedBy)
	r.Equal(env.demo.UID, *check.CreatedBy, "and they are stamped as its own")
}

// TestServeStdioSurvivesAPanickingTool: a tool that panics costs one internal
// error, and the next request is still served.
func TestServeStdioSurvivesAPanickingTool(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newStdioEnv(t)
	env.handler.toolMap[toolListChecks] = func(context.Context, string, map[string]any) ToolCallResult {
		panic("acme tool exploded")
	}

	principal, err := ResolveStdioPrincipal(t.Context(), env.db, "acme", "")
	r.NoError(err)

	msgs := runStdio(t, env, principal,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_checks"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	)
	r.Len(msgs, 2)
	r.JSONEq(`1`, string(msgs[0]["id"]))
	r.Equal(CodeInternalError, stdioError(t, msgs[0]).Code)
	r.JSONEq(`2`, string(msgs[1]["id"]))
}

// TestServeStdioStopsOnCancel: a canceled context ends the session even while
// stdin stays open.
func TestServeStdioStopsOnCancel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newStdioEnv(t)

	principal, err := ResolveStdioPrincipal(t.Context(), env.db, "acme", "")
	r.NoError(err)

	ctx, cancel := context.WithCancel(t.Context())
	blocked := &blockingReader{release: make(chan struct{})}

	t.Cleanup(func() { close(blocked.release) })

	done := make(chan error, 1)

	go func() { done <- env.handler.ServeStdio(ctx, blocked, &bytes.Buffer{}, principal) }()

	cancel()

	select {
	case err := <-done:
		r.NoError(err)
	case <-time.After(10 * time.Second):
		r.Fail("ServeStdio did not return after its context was canceled")
	}
}

// blockingReader never yields data until released, like an idle stdin.
type blockingReader struct {
	release chan struct{}
}

func (b *blockingReader) Read([]byte) (int, error) {
	<-b.release

	return 0, io.EOF
}
