package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecretFieldsIncludeCommunity(t *testing.T) {
	t.Parallel()

	require.ElementsMatch(t,
		[]string{"community", "authPassword", "privPassword"},
		(&SNMPConfig{}).SecretFields())
}
