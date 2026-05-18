package checkhttp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestAllDefaultSamplesValidate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	checker := &HTTPChecker{}

	for _, spec := range checker.GetSampleConfigs(nil) {
		spec := spec // capture loop variable
		t.Run(spec.Slug, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			cfg := &HTTPConfig{}
			r.NoError(cfg.FromMap(spec.Config), "spec: %s", spec.Slug)

			specCopy := &checkerdef.CheckSpec{
				Name:   spec.Name,
				Slug:   spec.Slug,
				Period: spec.Period,
				Config: spec.Config,
			}
			r.NoError(checker.Validate(specCopy), "spec: %s", spec.Slug)
		})
	}

	_ = r // r used for top-level assertions if needed
}
