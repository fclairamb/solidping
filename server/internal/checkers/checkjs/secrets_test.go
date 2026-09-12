package checkjs

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// TestSecretsIsTheOnlyDeclaredSecretField pins the split the spec is built on:
// `secrets` is encrypted, `env` deliberately is not. Declaring `env` here would
// sweep every plaintext script parameter into the encrypted column and out of
// exports — the exact outcome the spec rejects.
func TestSecretsIsTheOnlyDeclaredSecretField(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &JSConfig{}
	r.Equal([]string{"secrets"}, credentials.SecretFieldsFor(cfg))

	// And the generic splitter therefore moves exactly that key.
	full := map[string]any{
		"script":  "return {status:'up'}",
		"env":     map[string]any{"BASE_URL": "https://acme.com"},
		"secrets": map[string]any{"PASSWORD": "hunter2"},
	}

	public, private := credentials.SplitConfig(full, credentials.SecretFieldsFor(cfg))

	r.Contains(public, "env")
	r.Contains(public, "script")
	r.NotContains(public, "secrets", "the secrets map must leave the public config")
	r.Contains(private, "secrets")
	r.NotContains(private, "env", "env must stay public and diffable")
}

func TestSecretsRoundTripThroughTheConfigMap(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &JSConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"script":  "return {status:'up'}",
		"env":     map[string]any{"BASE_URL": "https://acme.com"},
		"secrets": map[string]any{"PASSWORD": "hunter2"},
	}))

	r.Equal(map[string]string{"BASE_URL": "https://acme.com"}, cfg.Env)
	r.Equal(map[string]string{"PASSWORD": "hunter2"}, cfg.Secrets)
	r.NoError(cfg.Validate())

	emitted := cfg.GetConfig()
	r.Equal(map[string]any{"BASE_URL": "https://acme.com"}, emitted["env"])
	r.Equal(map[string]any{"PASSWORD": "hunter2"}, emitted["secrets"])

	// An absent map stays absent rather than becoming an empty object, so an
	// untouched config round-trips without ever gaining a key.
	bare := &JSConfig{}
	r.NoError(bare.FromMap(map[string]any{"script": "return {status:'up'}"}))
	r.NotContains(bare.GetConfig(), "env")
	r.NotContains(bare.GetConfig(), "secrets")

	// Wrong shapes are rejected on both maps, identically.
	for _, key := range []string{"env", "secrets"} {
		bad := &JSConfig{}
		r.Error(bad.FromMap(map[string]any{"script": "x", key: "not-a-map"}), key)
		r.Error(bad.FromMap(map[string]any{"script": "x", key: map[string]any{"K": 1}}), key)
	}
}

func TestSecretsEntryLimit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &JSConfig{Script: "return {status:'up'}", Secrets: map[string]string{}}
	for i := range maxEnvEntries + 1 {
		cfg.Secrets["K"+itoa(i)] = "v"
	}

	err := cfg.Validate()
	r.Error(err)
	r.Contains(err.Error(), "secrets")
}

// TestScriptSeesBothMapsUnderTheirOwnNames is the engine half of the split: a
// script reads `secrets.X` and `env.Y`, and neither name leaks into the other
// object — which is what makes `secrets` a readable call-site signal rather
// than a second spelling of env.
func TestScriptSeesBothMapsUnderTheirOwnNames(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &JSChecker{}

	result, err := checker.Execute(t.Context(), &JSConfig{
		Script: `return { status: "up", output: {
  secret: secrets.PASSWORD,
  param: env.BASE_URL,
  secretInEnv: typeof env.PASSWORD,
  paramInSecrets: typeof secrets.BASE_URL,
} };`,
		Env:     map[string]string{"BASE_URL": "https://acme.com"},
		Secrets: map[string]string{"PASSWORD": "hunter2"},
	})
	r.NoError(err)

	r.Equal("hunter2", result.Output["secret"])
	r.Equal("https://acme.com", result.Output["param"])
	r.Equal("undefined", result.Output["secretInEnv"])
	r.Equal("undefined", result.Output["paramInSecrets"])
}
