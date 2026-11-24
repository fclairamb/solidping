package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkmongodb/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// TestSealedPasswordBoundToUsername (GHSA-38m7-jhhr-79xx): a blob sealed for
// one username fails once only the username changes.
func TestSealedPasswordBoundToUsername(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.MongoDBConfig{}
	fields := credentials.TargetFieldsFor(cfg)
	r.Contains(fields, "username")

	configA := map[string]any{"host": "db.acme.com", "port": 27017, "username": "monitor"}
	plain := credentials.WithBinding(map[string]any{"password": "s3cret"}, "mongodb", fields, configA)

	check := credentials.BindingCheck{
		Type: "mongodb", Config: configA, TargetFields: fields, SecretFields: cfg.SecretFields(),
	}
	secrets, bound, err := credentials.VerifyBinding(plain, check)
	r.NoError(err)
	r.True(bound)
	r.Equal("s3cret", secrets["password"])

	configB := map[string]any{"host": "db.acme.com", "port": 27017, "username": "x@evil.acme.com/?appname="}
	check.Config = configB
	_, _, err = credentials.VerifyBinding(plain, check)
	r.ErrorIs(err, credentials.ErrSealedForAnotherTarget)

	// A blob bound before `username` joined the list is stale once a
	// username is set, so the Re-seal page lists it.
	r.True(credentials.BindingStale(credentials.LegacyTargetFieldsFor(cfg), fields, configA))
}
