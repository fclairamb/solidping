package secretref

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// mapParamStore serves org parameters from a map keyed by storage key, as
// stored: it does not open sealed envelopes (an older or key-less reader).
type mapParamStore map[string]any

func (m mapParamStore) GetOrgParameter(_ context.Context, _, key string) (*models.Parameter, error) {
	value, ok := m[key]
	if !ok {
		return nil, nil //nolint:nilnil // a missing parameter, as the DB service reports it
	}

	return &models.Parameter{Key: key, Value: models.JSONMap{models.ParameterValueKey: value}}, nil
}

// TestStringValueScalars keeps the string form of every scalar a parameter
// can hold.
func TestStringValueScalars(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for raw, want := range map[any]string{
		"acme":             "acme",
		true:               "true",
		false:              "false",
		float64(42):        "42",
		1.5:                "1.5",
		int64(-7):          "-7",
		json.Number("123"): "123",
	} {
		got, ok := StringValue(&models.Parameter{Value: models.JSONMap{models.ParameterValueKey: raw}})
		r.True(ok, "%v", raw)
		r.Equal(want, got)
	}
}

// TestStringValueRejectsNonScalars covers spec 2026-11-28-01: a map or a slice
// has no string form, and is reported unresolvable rather than printed.
func TestStringValueRejectsNonScalars(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, raw := range []any{
		map[string]any{"$sp_sealed": `{"v":1}`},
		[]any{"a", "b"},
		nil,
	} {
		got, ok := StringValue(&models.Parameter{Value: models.JSONMap{models.ParameterValueKey: raw}})
		r.False(ok, "%v", raw)
		r.Empty(got)
	}
}

// TestLookupParamSealedEnvelopeUnresolved: a sealed row the store hands back
// unopened yields ErrUnresolved, never a string holding `map[`.
func TestLookupParamSealedEnvelopeUnresolved(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	store := mapParamStore{
		paramkeys.StorageKey("password"): map[string]any{"$sp_sealed": `{"v":1,"alg":"AES-256-GCM"}`},
	}

	value, err := LookupParam(context.Background(), store, "org-uid", "password")
	r.ErrorIs(err, ErrUnresolved)
	r.NotContains(value, "map[")
	r.Empty(value)
}
