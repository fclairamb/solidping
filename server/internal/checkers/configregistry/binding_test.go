package configregistry_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/plugins"
)

// TestTargetFieldsAreNeverSecret: the target fields a checker declares are
// never one of its secret fields, otherwise a sealed blob could not be bound
// to them.
func TestTargetFieldsAreNeverSecret(t *testing.T) {
	t.Parallel()

	declared := 0

	for _, checkType := range append(checkerdef.AdvertisedCheckTypes(), checkerdef.CheckTypeSleep) {
		cfg, ok := configregistry.ParseConfig(checkType)
		if !ok {
			continue
		}

		fielder, ok := cfg.(credentials.TargetFielder)
		if !ok {
			continue
		}

		declared++

		fields := fielder.TargetFields()
		require.NotEmpty(t, fields, checkType)

		secrets := credentials.SecretFieldsFor(cfg)

		for _, field := range fields {
			require.NotEmpty(t, field, checkType)
			require.NotContains(t, secrets, field, "%s: target field is a secret", checkType)
		}
	}

	require.Positive(t, declared)
}

func TestBindingCheckFor(t *testing.T) {
	t.Parallel()

	config := map[string]any{"url": "https://acme.com"}

	known := configregistry.BindingCheckFor("http", config)
	require.Equal(t, "http", known.Type)
	require.Equal(t, config, known.Config)
	require.Equal(t, []string{"tunnelCheckUid", "url"}, known.TargetFields)
	require.NotNil(t, known.SecretFields, "a known type lists its secret fields")

	unknown := configregistry.BindingCheckFor("acme-plugin", config)
	require.Equal(t, credentials.TargetFieldsFor(nil), unknown.TargetFields)
	require.Nil(t, unknown.SecretFields, "an unknown type has no secret field list")
}

// TestBindingCoversJSEnvAndPluginKeys: the regression of spec 2026-11-12-02.
// A JS blob is bound to the script AND its env (the base URL of the request
// carrying the secrets); a plugin, or a type unknown to the agent, to every
// public key but the timeout.
func TestBindingCoversJSEnvAndPluginKeys(t *testing.T) {
	t.Parallel()

	secrets := map[string]any{"secrets": map[string]any{"TOKEN": "hunter2"}}

	t.Run("js env", func(t *testing.T) {
		t.Parallel()

		sealedFor := map[string]any{
			"script":  "return http.get(env.BASE_URL, { headers: { Authorization: secrets.TOKEN } }).status === 200;",
			"env":     map[string]any{"BASE_URL": "https://api.acme.com"},
			"timeout": "10s",
		}
		check := configregistry.BindingCheckFor("js", sealedFor)
		require.Equal(t, []string{"env", "script", "tunnelCheckUid"}, check.TargetFields)
		plain := credentials.WithBinding(secrets, "js", check.TargetFields, sealedFor)

		moved := map[string]any{
			"script":  sealedFor["script"],
			"env":     map[string]any{"BASE_URL": "https://evilco.example"},
			"timeout": "10s",
		}
		_, _, err := credentials.VerifyBinding(plain, configregistry.BindingCheckFor("js", moved))
		require.ErrorIs(t, err, credentials.ErrSealedForAnotherTarget)
		require.ErrorContains(t, err, "type them again")

		slower := map[string]any{"script": sealedFor["script"], "env": sealedFor["env"], "timeout": "25s"}
		got, bound, err := credentials.VerifyBinding(plain, configregistry.BindingCheckFor("js", slower))
		require.NoError(t, err)
		require.True(t, bound)
		require.Equal(t, secrets, got)
	})

	t.Run("js empty env keeps the script-only digest", func(t *testing.T) {
		t.Parallel()

		script := map[string]any{"script": "return true;"}
		withEmptyEnv := map[string]any{"script": "return true;", "env": map[string]any{}}
		check := configregistry.BindingCheckFor("js", withEmptyEnv)
		require.Equal(t,
			credentials.TargetDigest([]string{"script", "tunnelCheckUid"}, script),
			credentials.TargetDigest(check.TargetFields, withEmptyEnv))
		require.False(t, credentials.BindingStale(
			credentials.LegacyTargetFieldsFor(mustParse(t, "js")), check.TargetFields, withEmptyEnv))
		require.True(t, credentials.BindingStale(
			credentials.LegacyTargetFieldsFor(mustParse(t, "js")), check.TargetFields,
			map[string]any{"script": "return true;", "env": map[string]any{"BASE_URL": "https://api.acme.com"}}))
	})

	t.Run("plugin keys", func(t *testing.T) {
		t.Parallel()

		pluginSecrets := map[string]any{"password": "hunter2"}
		sealedFor := map[string]any{"target": "mail.acme.com", "warning": "2", "timeout": "5s"}
		fields := credentials.TargetFieldsFor(&plugins.Config{})
		require.Equal(t, []string{credentials.AllPublicKeys, "tunnelCheckUid"}, fields)

		plain := credentials.WithBinding(pluginSecrets, "x-acme", fields, sealedFor)
		check := func(config map[string]any) credentials.BindingCheck {
			return credentials.BindingCheck{
				Type: "x-acme", Config: config, TargetFields: fields, SecretFields: []string{"password"},
			}
		}

		_, _, err := credentials.VerifyBinding(plain,
			check(map[string]any{"target": "evilco.example", "warning": "2", "timeout": "5s"}))
		require.ErrorIs(t, err, credentials.ErrSealedForAnotherTarget)

		got, _, err := credentials.VerifyBinding(plain,
			check(map[string]any{"target": "mail.acme.com", "warning": "2", "timeout": "9s"}))
		require.NoError(t, err)
		require.Equal(t, pluginSecrets, got)

		// A type the agent does not know binds the same way.
		unknown := configregistry.BindingCheckFor("x-acme",
			map[string]any{"target": "evilco.example", "warning": "2", "timeout": "5s"})
		_, _, err = credentials.VerifyBinding(plain, unknown)
		require.ErrorIs(t, err, credentials.ErrSealedForAnotherTarget)
	})
}

func mustParse(t *testing.T, checkType string) checkerdef.Config {
	t.Helper()

	cfg, ok := configregistry.ParseConfig(checkerdef.CheckType(checkType))
	require.True(t, ok)

	return cfg
}
