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

func TestIsMutationTool(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want bool
	}{
		{name: "create_check", want: true},
		{name: "update_check", want: true},
		{name: "delete_check", want: true},
		{name: "set_maintenance_window_checks", want: true},
		{name: "list_checks", want: false},
		{name: "get_check", want: false},
		{name: "diagnose_check", want: false},
		{name: "validate_check", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			r.Equal(tc.want, isMutationTool(tc.name))
		})
	}
}
