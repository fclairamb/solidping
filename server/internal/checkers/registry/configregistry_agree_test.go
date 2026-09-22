package registry

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
)

// TestRegistriesAgree pins the invariant the split rests on: the heavy registry
// (which owns GetChecker, and with it every execution client) and the light
// configregistry (which `sp checks validate` links instead) implement exactly
// the same set of check types.
//
// It is the only thing standing between the two switches and a silent
// divergence: a checker added to GetChecker but not to configregistry would
// make `sp checks validate` answer "unsupported check type" about a type the
// server happily creates, and the reverse would have the CLI validate a type no
// worker can run. ParseConfig delegating to the light registry covers the
// config half by construction; this covers the membership half.
func TestRegistriesAgree(t *testing.T) {
	t.Parallel()

	types := checkerdef.ListCheckTypes(nil)
	require.NotEmpty(t, types, "checkerdef must declare at least one check type")

	for _, checkType := range types {
		_, heavy := GetChecker(checkType)
		light := configregistry.IsKnownType(checkType)

		require.Equalf(t, heavy, light,
			"check type %q: GetChecker=%v but configregistry.IsKnownType=%v — "+
				"add it to BOTH switches", checkType, heavy, light)
	}
}

// TestRegistriesAgreeOnEveryGetCheckerType walks the direction ListCheckTypes
// cannot cover: a type wired into GetChecker but never declared in
// checkerdef's list would be invisible to the test above.
func TestRegistriesAgreeOnEveryGetCheckerType(t *testing.T) {
	t.Parallel()

	for _, checkType := range checkerdef.ListCheckTypes(nil) {
		if _, ok := GetChecker(checkType); !ok {
			continue
		}

		cfg, ok := configregistry.ParseConfig(checkType)
		require.Truef(t, ok, "check type %q has a checker but no config in configregistry", checkType)
		require.NotNilf(t, cfg, "check type %q returned a nil config", checkType)

		require.NoErrorf(t, validateSpecIsWired(checkType), "check type %q is not wired into ValidateSpec", checkType)
	}
}

// validateSpecIsWired asserts configregistry.ValidateSpec has a case for
// checkType — it must not fall through to ErrUnknownType. An empty config is
// invalid for most types, so the assertion is on the SENTINEL, not on success.
func validateSpecIsWired(checkType checkerdef.CheckType) error {
	err := configregistry.ValidateSpec(checkType, &checkerdef.CheckSpec{Config: map[string]any{}})
	if err != nil && err.Error() == configregistry.ErrUnknownType.Error() {
		return err
	}

	return nil
}
