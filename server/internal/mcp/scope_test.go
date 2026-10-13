package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

func TestHasMCPAccess(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		claims *auth.Claims
		want   bool
	}{
		{name: "nil claims rejected", claims: nil, want: false},
		{name: "empty scopes treated as full session", claims: &auth.Claims{}, want: true},
		{name: "explicit mcp scope allowed", claims: &auth.Claims{Scopes: []string{"mcp"}}, want: true},
		{name: "explicit mcp:read scope allowed", claims: &auth.Claims{Scopes: []string{"mcp:read"}}, want: true},
		{name: "unrelated scopes only refused", claims: &auth.Claims{Scopes: []string{"checks:write"}}, want: false},
		{name: "mcp combined with others allowed", claims: &auth.Claims{Scopes: []string{"checks:read", "mcp"}}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			r.Equal(tc.want, hasMCPAccess(tc.claims))
		})
	}
}

func TestIsMCPReadOnly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		claims *auth.Claims
		want   bool
	}{
		{name: "nil claims not read-only", claims: nil, want: false},
		{name: "empty scopes not read-only", claims: &auth.Claims{}, want: false},
		{name: "mcp:read alone is read-only", claims: &auth.Claims{Scopes: []string{"mcp:read"}}, want: true},
		{name: "mcp alone not read-only", claims: &auth.Claims{Scopes: []string{"mcp"}}, want: false},
		{name: "mcp wins over mcp:read", claims: &auth.Claims{Scopes: []string{"mcp:read", "mcp"}}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			r.Equal(tc.want, isMCPReadOnly(tc.claims))
		})
	}
}

func TestToolMutates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args map[string]any
		want bool
	}{
		{name: "create_check", want: true},
		{name: "update_check", want: true},
		{name: "delete_check", want: true},
		{name: "run_check", want: true},
		{name: "set_maintenance_window_checks", want: true},
		{name: "list_checks", want: false},
		{name: "get_check", want: false},
		{name: "diagnose_check", want: false},
		{name: "validate_check", want: false},
		// Fail closed: a name with no entry in the table is a write.
		{name: "no_such_tool", want: true},
		{name: "", want: true},
		{name: "LIST_CHECKS", want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			r.Equal(tc.want, toolMutates(tc.name, tc.args))
		})
	}
}

// TestToolMutatesByAction: a by-action tool is a write unless its action is
// one of the declared read actions, exactly spelled.
func TestToolMutatesByAction(t *testing.T) {
	t.Parallel()

	const fakeTool = "test_by_action_tool"

	access := byActionTool("peek")

	cases := []struct {
		name string
		args map[string]any
		want bool
	}{
		{name: "declared read action", args: map[string]any{propAction: "peek"}, want: false},
		{name: "write action", args: map[string]any{propAction: "delete"}, want: true},
		{name: "missing action", args: map[string]any{}, want: true},
		{name: "nil args", args: nil, want: true},
		{name: "non-string action", args: map[string]any{propAction: 3}, want: true},
		{name: "unknown action", args: map[string]any{propAction: "frobnicate"}, want: true},
		{name: "uppercase read action", args: map[string]any{propAction: "Peek"}, want: true},
		{name: "padded read action", args: map[string]any{propAction: " peek"}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, accessMutates(access, tc.args), fakeTool)
		})
	}
}

// TestEveryToolHasAnAccessEntry: every callable tool, listed or alias, has an
// explicit entry, and every entry names a callable tool.
func TestEveryToolHasAnAccessEntry(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()

	for name := range handler.toolMap {
		_, ok := toolAccessTable[name]
		r.Truef(ok, "tool %q has no entry in toolAccessTable", name)
	}

	for name := range toolAccessTable {
		_, ok := handler.toolMap[name]
		r.Truef(ok, "toolAccessTable entry %q names no registered tool", name)
	}
}
