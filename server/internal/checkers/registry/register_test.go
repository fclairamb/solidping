package registry

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksleep"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
)

// Every built-in type with a meta still resolves to a checker and a config of
// its own type through the registration map.
func TestEveryBuiltinTypeResolves(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	builtins := 0

	for _, meta := range checkerdef.ListCheckTypeMetas() {
		if meta.Plugin != nil {
			continue
		}

		builtins++

		checker, ok := GetChecker(meta.Type)
		r.True(ok, "GetChecker(%s)", meta.Type)
		r.Equal(meta.Type, checker.Type())

		cfg, ok := ParseConfig(meta.Type)
		r.True(ok, "ParseConfig(%s)", meta.Type)

		lightCfg, ok := configregistry.ParseConfig(meta.Type)
		r.True(ok)
		r.IsType(lightCfg, cfg, "registry and configregistry agree on %s", meta.Type)
	}

	r.Len(builtinCheckers, builtins)
}

func TestRegisterDuplicateTypePanics(t *testing.T) {
	t.Parallel()

	meta := *checkerdef.GetCheckTypeMeta(checkerdef.CheckTypeSleep)

	require.Panics(t, func() {
		Register(meta,
			func() checkerdef.Checker { return &checksleep.SleepChecker{} },
			func() checkerdef.Config { return nil })
	})
}

func TestRegisterPluginRefusesABuiltinType(t *testing.T) {
	t.Parallel()

	meta := *checkerdef.GetCheckTypeMeta(checkerdef.CheckTypeSleep)

	err := RegisterPlugin(meta,
		func() checkerdef.Checker { return &checksleep.SleepChecker{} },
		func() checkerdef.Config { return nil },
		func(*checkerdef.CheckSpec) error { return nil })
	require.ErrorIs(t, err, ErrDuplicateType)
}
