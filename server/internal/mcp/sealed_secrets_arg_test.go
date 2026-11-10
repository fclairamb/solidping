package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSealedSecretsArg: create_check/update_check pass sealedSecrets as is
// (spec 2026-11-08-01 §9); absent stays nil.
func TestSealedSecretsArg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Nil(sealedSecretsArg(map[string]any{}))

	got := sealedSecretsArg(map[string]any{"sealedSecrets": map[string]any{
		"keys": []any{"basicAuth"}, "fingerprints": []any{"aa"}, "ct": "Y3Q=",
	}})
	r.NotNil(got)
	r.Equal([]string{"basicAuth"}, got.Keys)
	r.Equal([]string{"aa"}, got.Fingerprints)
	r.Equal("Y3Q=", got.CT)
}
