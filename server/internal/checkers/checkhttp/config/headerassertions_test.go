package config

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func hLeaf(path, op, value string, ignoreCase bool) *AssertionNode {
	return &AssertionNode{
		Type: NodeTypeAssertion, Path: path, Operator: op, Value: value, IgnoreCase: ignoreCase,
	}
}

func TestValidateHeaders(t *testing.T) {
	t.Parallel()

	bad := map[string]*AssertionNode{
		"empty name":      hLeaf("", OpEq, "x", false),
		"invalid name":    hLeaf("Bad Name:", OpEq, "x", false),
		"gt":              hLeaf("X-A", OpGt, "1", false),
		"eq no value":     hLeaf("X-A", OpEq, "", false),
		"exists value":    hLeaf("X-A", OpExists, "x", false),
		"bad regex":       hLeaf("X-A", OpRegex, "(", false),
		"empty group":     {Type: NodeTypeAnd},
		"nested bad leaf": {Type: NodeTypeOr, Children: []AssertionNode{*hLeaf("X-A", OpGte, "1", false)}},
	}

	for name, node := range bad {
		t.Run("rejects "+name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, node.ValidateHeaders())
		})
	}

	good := []*AssertionNode{
		hLeaf("X-A", OpEq, "x", false), hLeaf("X-A", OpNeq, "x", false),
		hLeaf("X-A", OpContains, "x", false), hLeaf("X-A", OpNotContains, "x", false),
		hLeaf("X-A", OpRegex, "^x", false), hLeaf("X-A", OpExists, "", false),
		hLeaf("X-A", OpNotExists, "", false),
	}

	for _, node := range good {
		t.Run("accepts "+node.Operator, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, node.ValidateHeaders())
		})
	}
}

func TestEvaluateHeaders(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	h.Add("Cache-Control", "public, max-age=60")
	h.Add("Content-Type", "Application/JSON")
	h.Add("X-Acme", "one")
	h.Add("X-Acme", "two-secure")
	h.Add("Set-Cookie", "sid=abc; Secure")

	tests := []struct {
		name string
		node *AssertionNode
		pass bool
	}{
		{"eq pass", hLeaf("Content-Type", OpEq, "Application/JSON", false), true},
		{"eq fail", hLeaf("Content-Type", OpEq, "application/json", false), false},
		{"eq ignoreCase pass", hLeaf("Content-Type", OpEq, "application/json", true), true},
		{"neq pass", hLeaf("Content-Type", OpNeq, "text/html", false), true},
		{"neq fail", hLeaf("Content-Type", OpNeq, "Application/JSON", false), false},
		{"contains pass", hLeaf("Cache-Control", OpContains, "max-age", false), true},
		{"contains fail", hLeaf("Cache-Control", OpContains, "no-store", false), false},
		{"not_contains pass", hLeaf("Cache-Control", OpNotContains, "no-store", false), true},
		{"not_contains fail", hLeaf("Cache-Control", OpNotContains, "public", false), false},
		{"regex pass", hLeaf("Cache-Control", OpRegex, `max-age=\d+`, false), true},
		{"regex fail", hLeaf("Cache-Control", OpRegex, `^max-age`, false), false},
		{"exists pass", hLeaf("Cache-Control", OpExists, "", false), true},
		{"exists fail", hLeaf("X-Powered-By", OpExists, "", false), false},
		{"not_exists pass", hLeaf("X-Powered-By", OpNotExists, "", false), true},
		{"not_exists fail", hLeaf("Cache-Control", OpNotExists, "", false), false},
		{"absent eq fails", hLeaf("X-Missing", OpEq, "x", false), false},
		{"absent contains fails", hLeaf("X-Missing", OpContains, "x", false), false},
		{"absent regex fails", hLeaf("X-Missing", OpRegex, "x", false), false},
		{"absent neq passes", hLeaf("X-Missing", OpNeq, "x", false), true},
		{"absent not_contains passes", hLeaf("X-Missing", OpNotContains, "x", false), true},
		{"lowercase name", hLeaf("cache-control", OpContains, "public", false), true},
		{"repeated contains second only", hLeaf("X-Acme", OpContains, "secure", false), true},
		{"repeated not_contains second only", hLeaf("X-Acme", OpNotContains, "secure", false), false},
		{"repeated not_contains none", hLeaf("X-Acme", OpNotContains, "zzz", false), true},
		{"or group", &AssertionNode{Type: NodeTypeOr, Children: []AssertionNode{
			*hLeaf("X-Missing", OpExists, "", false), *hLeaf("X-Acme", OpExists, "", false),
		}}, true},
		{"and group", &AssertionNode{Type: NodeTypeAnd, Children: []AssertionNode{
			*hLeaf("X-Missing", OpExists, "", false), *hLeaf("X-Acme", OpExists, "", false),
		}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.pass, tt.node.EvaluateHeaders(h).Pass)
		})
	}

	t.Run("set-cookie actual is redacted but compared for real", func(t *testing.T) {
		t.Parallel()

		res := hLeaf("Set-Cookie", OpContains, "Secure", false).EvaluateHeaders(h)
		require.True(t, res.Pass)
		require.Equal(t, RedactedMarker, res.Actual)
	})

	t.Run("actual joins values", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "one, two-secure", hLeaf("X-Acme", OpExists, "", false).EvaluateHeaders(h).Actual)
	})

	t.Run("absent error", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "header not found", hLeaf("X-Missing", OpEq, "x", false).EvaluateHeaders(h).Error)
	})
}

func TestHeaderAssertions_RoundTrip(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"headerAssertions", "header_assertions"} {
		cfg := &HTTPConfig{}
		require.NoError(t, cfg.FromMap(map[string]any{
			"url": "http://example.com",
			key: map[string]any{
				"type": "assertion", "path": "X-Powered-By", "operator": "not_exists",
			},
		}))
		require.NotNil(t, cfg.HeaderAssertions, key)
		require.Equal(t, "X-Powered-By", cfg.HeaderAssertions.Path)

		out := cfg.GetConfig()
		m, ok := out["headerAssertions"].(map[string]any)
		require.True(t, ok, key)
		require.Equal(t, "not_exists", m["operator"])
	}
}
