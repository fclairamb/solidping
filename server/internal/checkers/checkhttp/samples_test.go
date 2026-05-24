package checkhttp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestAllDefaultSamplesValidate(t *testing.T) {
	t.Parallel()

	checker := &HTTPChecker{}

	for _, spec := range checker.GetSampleConfigs(nil) {
		t.Run(spec.Slug, func(t *testing.T) {
			t.Parallel()

			rt := require.New(t)

			cfg := &HTTPConfig{}
			rt.NoError(cfg.FromMap(spec.Config), "spec: %s", spec.Slug)

			specCopy := &checkerdef.CheckSpec{
				Name:   spec.Name,
				Slug:   spec.Slug,
				Period: spec.Period,
				Config: spec.Config,
			}
			rt.NoError(checker.Validate(specCopy), "spec: %s", spec.Slug)
		})
	}
}
