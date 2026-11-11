package configregistry_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
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
