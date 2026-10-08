package checkversion_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/checkversion"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestSnapshotNeverHoldsSecrets: whatever the row holds, plaintext secrets in
// config (no master key), an encrypted envelope or a sealed blob, the snapshot
// carries none of it.
func TestSnapshotNeverHoldsSecrets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		checkType string
		config    models.JSONMap
	}{
		{"js", models.JSONMap{"script": "return 1", "secrets": map[string]any{"token": "s3cret"}}},
		{"postgresql", models.JSONMap{"host": "db.acme.com", "username": "alice", "password": "s3cret"}},
		{"ssh", models.JSONMap{"host": "ssh.acme.com", "password": "s3cret", "private_key": "-----BEGIN"}},
	}

	for _, tc := range cases {
		t.Run(tc.checkType, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			cfg, ok := configregistry.ParseConfig(checkerdef.CheckType(tc.checkType))
			r.True(ok)

			secretKeys := credentials.SecretFieldsFor(cfg)
			r.NotEmpty(secretKeys, "the table only covers types with secrets")

			privateKeys := `["extra_private"]`
			envelope := "envelope"
			sealed := "sealed"

			check := models.NewCheck("org", "slug", tc.checkType)
			check.Config = tc.config
			check.Config["extra_private"] = "also-secret"
			check.ConfigPrivate = &envelope
			check.ConfigPrivateKeys = &privateKeys
			check.ConfigSealed = &sealed

			snap := checkversion.Build(check, map[string]string{"env": "prod"})

			encoded, err := snap.Canonical()
			r.NoError(err)

			for _, key := range append(secretKeys, "extra_private") {
				r.NotContains(snap.Config, key)
			}

			for _, forbidden := range []string{
				"s3cret", "also-secret", "envelope", "sealed", "-----BEGIN",
				"config_private", "configPrivate", "config_sealed",
			} {
				r.NotContains(string(encoded), forbidden)
			}

			r.Equal(map[string]string{"env": "prod"}, snap.Labels)
		})
	}
}

func TestSnapshotHashIsStableAndSensitive(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	check := models.NewCheck("org", "web", "http")
	check.Config = models.JSONMap{"url": "https://acme.com", "method": "GET"}

	first, err := checkversion.Build(check, nil).Hash()
	r.NoError(err)

	again, err := checkversion.Build(check, nil).Hash()
	r.NoError(err)
	r.Equal(first, again)

	// A round trip through the stored column hashes the same.
	stored, err := checkversion.Build(check, nil).ToJSONMap()
	r.NoError(err)

	encoded, err := json.Marshal(stored)
	r.NoError(err)

	decoded := models.JSONMap{}
	r.NoError(json.Unmarshal(encoded, &decoded))

	back, err := checkversion.FromJSONMap(decoded)
	r.NoError(err)

	roundTrip, err := back.Hash()
	r.NoError(err)
	r.Equal(first, roundTrip)

	name := "renamed"
	check.Name = &name

	renamed, err := checkversion.Build(check, nil).Hash()
	r.NoError(err)
	r.NotEqual(first, renamed)
}

// TestSnapshotIgnoresAutoPlacementRegions: an auto check's region list is the
// scheduler's, so moving it records nothing.
func TestSnapshotIgnoresAutoPlacementRegions(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	check := models.NewCheck("org", "web", "http")
	check.Placement = models.PlacementAuto
	check.Regions = []string{"eu-west"}

	before, err := checkversion.Build(check, nil).Hash()
	r.NoError(err)

	check.Regions = []string{"us-east"}

	after, err := checkversion.Build(check, nil).Hash()
	r.NoError(err)
	r.Equal(before, after)
}
