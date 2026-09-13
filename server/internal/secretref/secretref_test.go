package secretref_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/secretref"
)

// TestGrammarRecognizesWhatItMustRecognize pins what a reference IS.
//
// It matters because almost every guarantee in spec 2026-09-11-03 is phrased as
// a negative — "the resolved value is not in the config", "the literal is not
// sent" — and a grammar that stopped recognizing `${param:…}` would make every
// one of those assertions trivially true while the feature silently did
// nothing.
func TestGrammarRecognizesWhatItMustRecognize(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(secretref.Contains("password=${param:sso-authtest-password}"))
	r.True(secretref.Contains("Bearer ${env:SP_TOKEN}"))
	r.False(secretref.Contains("password=hunter2"))
	r.False(secretref.Contains("${unknown:x}"))

	r.True(secretref.ContainsInConfig(map[string]any{
		"metadata": map[string]any{"authorization": "Bearer ${param:api_token}"},
	}), "a reference nested one level down is still a reference")

	r.True(secretref.ContainsInConfig(map[string]any{
		"args": []any{"--token", "${env:SP_TOKEN}"},
	}), "a reference inside a slice is still a reference")
}

// TestResolveConfigRecursesEverywhereContainsDoes is the property the spec-03
// audit found broken on the write path: validation walked only top-level
// strings while execution recursed, so a reference inside a gRPC check's
// `metadata` map passed dry run and failed later. The two must agree, so both
// sides now call ResolveConfig — and this pins that it really does descend.
func TestResolveConfigRecursesEverywhereContainsDoes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	resolve := func(_ context.Context, _, name string) (string, error) {
		return "resolved-" + name, nil
	}

	config := map[string]any{
		"top":       "${param:a}",
		"metadata":  map[string]any{"authorization": "Bearer ${param:b}"},
		"args":      []any{"--token", "${env:C}"},
		"untouched": 42,
	}

	out, replaced, err := secretref.ResolveConfig(t.Context(), config, resolve)
	r.NoError(err)
	r.True(replaced)

	r.Equal("resolved-a", out["top"])
	r.Equal([]any{"--token", "resolved-C"}, out["args"])
	r.Equal(42, out["untouched"])

	outMeta, ok := out["metadata"].(map[string]any)
	r.True(ok, "a nested map must come back as a map")
	r.Equal("Bearer resolved-b", outMeta["authorization"])

	// The input is never mutated: the caller usually holds a row's config map.
	r.Equal("${param:a}", config["top"])

	inMeta, ok := config["metadata"].(map[string]any)
	r.True(ok)
	r.Equal("Bearer ${param:b}", inMeta["authorization"])
}

// TestExecutionResolverRefusesParam pins the last line of defense: a
// `${param:}` still present when a check is about to run means the API could not
// resolve it, and the checker must never receive the literal.
// Uses t.Setenv, which is incompatible with t.Parallel.
func TestExecutionResolverRefusesParam(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_TEST_SECRETREF_TOKEN", "from-env")

	out, _, err := secretref.ResolveConfig(t.Context(),
		map[string]any{"a": "${env:SP_TEST_SECRETREF_TOKEN}"}, secretref.ExecutionResolver())
	r.NoError(err)
	r.Equal("from-env", out["a"])

	_, _, err = secretref.ResolveConfig(t.Context(),
		map[string]any{"a": "${param:anything}"}, secretref.ExecutionResolver())
	r.ErrorIs(err, secretref.ErrUnresolved)
	r.Contains(err.Error(), "unresolved secret reference: param:anything")
}

// TestAPIResolverSkipsEnv is the mirror image: the API resolves only what it can
// read, and hands `${env:}` on untouched so a deported agent resolves it against
// its OWN environment.
// Uses t.Setenv, which is incompatible with t.Parallel.
func TestAPIResolverSkipsEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_TEST_SECRETREF_TOKEN", "the-api-pods-value")

	out, replaced, err := secretref.ResolveConfig(t.Context(),
		map[string]any{"a": "${env:SP_TEST_SECRETREF_TOKEN}"},
		secretref.APIResolver(nil, "org-1"))
	r.NoError(err)
	r.False(replaced)
	r.Equal("${env:SP_TEST_SECRETREF_TOKEN}", out["a"],
		"the API must not substitute its own environment for the agent's")
}
