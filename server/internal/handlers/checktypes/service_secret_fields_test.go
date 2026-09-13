package checktypes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
)

// TestSecretFieldsForAdvertisesCheckerSecrets pins the contract the dashboard
// depends on (spec 2026-09-11-01): the check-types metadata names every config
// key a type stores encrypted, so the form's unmodeled-key passthrough can
// exclude them, and a type with no secrets advertises an empty list rather
// than null.
func TestSecretFieldsForAdvertisesCheckerSecrets(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	http := secretFieldsFor(checkerdef.CheckType("http"))
	r.Equal([]string{"basicAuth", "password", "secretHeaders"}, http)

	icmp := secretFieldsFor(checkerdef.CheckType("icmp"))
	r.NotNil(icmp)
	r.Empty(icmp)

	unknown := secretFieldsFor(checkerdef.CheckType("definitely-not-a-check-type"))
	r.NotNil(unknown)
	r.Empty(unknown)
}

// TestListServerCheckTypesCarriesSecretFields proves the field actually reaches
// the response payload (a struct field that is never populated is the failure
// mode this guards).
func TestListServerCheckTypesCarriesSecretFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	svc := NewService(checkerdef.NewActivationResolver(&config.CheckersConfig{}), "https://example.com")
	resp := svc.ListServerCheckTypes()
	r.NotEmpty(resp.Data)

	found := false

	for idx := range resp.Data {
		r.NotNil(resp.Data[idx].SecretFields, "type %q must advertise a non-nil secretFields", resp.Data[idx].Type)

		if resp.Data[idx].Type == "http" {
			found = true

			r.Equal([]string{"basicAuth", "password", "secretHeaders"}, resp.Data[idx].SecretFields)
		}
	}

	r.True(found, "http must be listed")
}
