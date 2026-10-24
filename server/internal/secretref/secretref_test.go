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

	allow := secretref.AllowEnvNames("SP_TEST_SECRETREF_TOKEN")

	out, _, err := secretref.ResolveConfig(t.Context(),
		map[string]any{"a": "${env:SP_TEST_SECRETREF_TOKEN}"}, secretref.ExecutionResolver(allow))
	r.NoError(err)
	r.Equal("from-env", out["a"])

	_, _, err = secretref.ResolveConfig(t.Context(),
		map[string]any{"a": "${param:anything}"}, secretref.ExecutionResolver(allow))
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

func TestGrammarRecognizesVar(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(secretref.Pattern.MatchString("https://acme.com/?cb=${var:uuid}"))

	m := secretref.Pattern.FindStringSubmatch("${var:uuid}")
	r.Equal([]string{"${var:uuid}", "var", "uuid"}, m)
}

func TestAPIResolverSkipsVar(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	out, replaced, err := secretref.ResolveConfig(t.Context(),
		map[string]any{"url": "https://acme.com/?cb=${var:uuid}"},
		secretref.APIResolver(nil, "org-1"))
	r.NoError(err)
	r.False(replaced)
	r.Equal("https://acme.com/?cb=${var:uuid}", out["url"],
		"the API leaves ${var:} for the executing process")
}

func TestContainsSecretRefIgnoresVar(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.False(secretref.ContainsSecretRef("https://acme.com/?cb=${var:timestamp}&id=${var:uuid}"))
	r.True(secretref.ContainsSecretRef("https://acme.com/?cb=${var:timestamp}&t=${param:tok}"))
	r.True(secretref.ContainsSecretRef("Bearer ${env:SP_TOKEN}"))
	r.False(secretref.ContainsSecretRef("plain"))
	r.True(secretref.Contains("${var:uuid}"), "Contains still sees every reference")
}

// TestEnvReferenceIsConfinedToTheAllowlist pins spec 2026-10-16-03: `${env:}`
// resolves only names the operator allowlisted, on the execution resolver and
// the document (import/apply) resolver alike. A refused name fails exactly like
// an unset one, so neither path tells the caller whether the variable exists.
// Uses t.Setenv, which is incompatible with t.Parallel.
func TestEnvReferenceIsConfinedToTheAllowlist(t *testing.T) {
	t.Setenv("SP_TEST_SECRETREF_ALLOWED", "allowed-value")
	t.Setenv("SP_TEST_SECRETREF_PLATFORM", "platform-secret")

	allow := secretref.AllowEnvNames("SP_TEST_SECRETREF_ALLOWED", "SP_TEST_SECRETREF_UNSET")

	resolvers := map[string]func(secretref.EnvAllowFunc) secretref.ResolveFunc{
		"execution": secretref.ExecutionResolver,
		"document": func(allowEnv secretref.EnvAllowFunc) secretref.ResolveFunc {
			return secretref.DocumentResolver(nil, "org-1", allowEnv)
		},
	}

	tests := []struct {
		name    string
		ref     string
		allow   secretref.EnvAllowFunc
		want    string
		wantErr string
	}{
		{name: "allowlisted name resolves", ref: "${env:SP_TEST_SECRETREF_ALLOWED}", allow: allow, want: "allowed-value"},
		{
			name: "set but not allowlisted is refused", ref: "${env:SP_TEST_SECRETREF_PLATFORM}", allow: allow,
			wantErr: "unresolved secret reference: env:SP_TEST_SECRETREF_PLATFORM",
		},
		{
			name: "nil allowlist refuses everything", ref: "${env:SP_TEST_SECRETREF_ALLOWED}", allow: nil,
			wantErr: "unresolved secret reference: env:SP_TEST_SECRETREF_ALLOWED",
		},
		{
			name: "allowlisted but unset fails the same way", ref: "${env:SP_TEST_SECRETREF_UNSET}", allow: allow,
			wantErr: "unresolved secret reference: env:SP_TEST_SECRETREF_UNSET",
		},
		{name: "var reference is unaffected", ref: "${var:timestamp}", allow: nil},
	}

	//nolint:paralleltest // the parent's t.Setenv forbids t.Parallel in subtests.
	for resolverName, build := range resolvers {
		for _, tt := range tests {
			t.Run(resolverName+"/"+tt.name, func(t *testing.T) {
				r := require.New(t)

				out, _, err := secretref.ResolveString(t.Context(), tt.ref, build(tt.allow))
				if tt.wantErr != "" {
					r.ErrorIs(err, secretref.ErrUnresolved)
					r.Equal(tt.wantErr, err.Error())
					r.NotContains(err.Error(), "platform-secret")

					return
				}

				r.NoError(err)

				if tt.want != "" {
					r.Equal(tt.want, out)
				} else {
					r.NotEmpty(out)
					r.NotContains(out, "${")
				}
			})
		}
	}

	// `${param:}` is still refused by the execution resolver whatever the
	// env allowlist says.
	_, _, err := secretref.ResolveString(t.Context(), "${param:SP_TEST_SECRETREF_ALLOWED}",
		secretref.ExecutionResolver(allow))
	require.ErrorIs(t, err, secretref.ErrUnresolved)
}

func TestDisallowedEnvRefs(t *testing.T) {
	t.Parallel()

	allow := secretref.AllowEnvNames(" SP_OK ", "")

	tests := []struct {
		name  string
		value string
		allow secretref.EnvAllowFunc
		want  []string
	}{
		{name: "no reference", value: "https://acme.com", allow: allow},
		{name: "allowlisted", value: "Bearer ${env:SP_OK}", allow: allow},
		{
			name: "mixed", value: "${env:SP_AUTH_JWT_SECRET}/${env:SP_OK}/${env:SP_DB_URL}", allow: allow,
			want: []string{"SP_AUTH_JWT_SECRET", "SP_DB_URL"},
		},
		{name: "nil allow refuses all", value: "${env:SP_OK}", allow: nil, want: []string{"SP_OK"}},
		{name: "param and var ignored", value: "${param:x} ${var:uuid}", allow: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, secretref.DisallowedEnvRefs(tt.value, tt.allow))
		})
	}
}
