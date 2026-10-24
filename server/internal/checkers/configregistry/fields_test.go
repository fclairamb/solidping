package configregistry_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
)

// jsonKeys lists the JSON keys of a config struct (top-level fields only),
// with a correctly typed sample value for the scalar ones.
func jsonKeys(cfg checkerdef.Config) map[string]any {
	typ := reflect.TypeOf(cfg)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	keys := map[string]any{}
	if typ.Kind() != reflect.Struct {
		return keys
	}

	for i := range typ.NumField() {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}

		switch typ.Field(i).Type.Kind() { //nolint:exhaustive // scalars only
		case reflect.String:
			keys[tag] = "x"
		case reflect.Bool:
			keys[tag] = true
		case reflect.Int, reflect.Int32, reflect.Int64, reflect.Float64:
			keys[tag] = float64(1)
		default:
			keys[tag] = nil
		}
	}

	return keys
}

// TestFromMapRejectsMalformedValues feeds every key of every registered
// config a value no field accepts. FromMap must never panic, and when it
// refuses it must say so with a ConfigError naming the parameter.
func TestFromMapRejectsMalformedValues(t *testing.T) {
	t.Parallel()

	types := append(checkerdef.AdvertisedCheckTypes(), checkerdef.CheckTypeSleep)

	rejected := 0

	for _, checkType := range types {
		cfg, ok := configregistry.ParseConfig(checkType)
		if !ok {
			continue
		}

		for key := range jsonKeys(cfg) {
			fresh, _ := configregistry.ParseConfig(checkType)

			var err error
			require.NotPanics(t, func() { err = fresh.FromMap(map[string]any{key: struct{ X int }{1}}) },
				"%s.%s", checkType, key)

			if err == nil {
				continue
			}

			rejected++

			var cfgErr *checkerdef.ConfigError
			require.True(t, errors.As(err, &cfgErr), "%s.%s: %v", checkType, key, err)
			require.NotEmpty(t, cfgErr.Parameter)
		}
	}

	require.Greater(t, rejected, 100, "most typed keys must reject a malformed value")
}

// TestFromMapAcceptsTypedValues checks that a correctly typed value for a
// scalar key either parses or is refused with a ConfigError (never a panic),
// and that a successful parse survives a GetConfig round trip.
func TestFromMapAcceptsTypedValues(t *testing.T) {
	t.Parallel()

	types := append(checkerdef.AdvertisedCheckTypes(), checkerdef.CheckTypeSleep)

	for _, checkType := range types {
		cfg, ok := configregistry.ParseConfig(checkType)
		if !ok {
			continue
		}

		for key, value := range jsonKeys(cfg) {
			if value == nil {
				continue
			}

			fresh, _ := configregistry.ParseConfig(checkType)

			var err error
			require.NotPanics(t, func() { err = fresh.FromMap(map[string]any{key: value}) }, "%s.%s", checkType, key)

			if err != nil {
				var cfgErr *checkerdef.ConfigError
				require.True(t, errors.As(err, &cfgErr), "%s.%s: %v", checkType, key, err)

				continue
			}

			again, _ := configregistry.ParseConfig(checkType)
			require.NotPanics(t, func() { _ = again.FromMap(fresh.GetConfig()) }, "%s.%s", checkType, key)
		}
	}
}

// TestGetConfigRoundTripWithEveryScalarSet fills every scalar key a config
// accepts at once, then checks the exported map parses back without error.
func TestGetConfigRoundTripWithEveryScalarSet(t *testing.T) {
	t.Parallel()

	types := append(checkerdef.AdvertisedCheckTypes(), checkerdef.CheckTypeSleep)

	for _, checkType := range types {
		cfg, ok := configregistry.ParseConfig(checkType)
		if !ok {
			continue
		}

		accepted := map[string]any{}

		for key, value := range jsonKeys(cfg) {
			if value == nil {
				continue
			}

			probe, _ := configregistry.ParseConfig(checkType)
			if probe.FromMap(map[string]any{key: value}) == nil {
				accepted[key] = value
			}
		}

		full, _ := configregistry.ParseConfig(checkType)
		if full.FromMap(accepted) != nil {
			continue // keys accepted alone may conflict together
		}

		exported := full.GetConfig()

		again, _ := configregistry.ParseConfig(checkType)
		require.NoError(t, again.FromMap(exported), "%s round trip", checkType)
	}
}
