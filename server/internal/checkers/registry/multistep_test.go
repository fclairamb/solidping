package registry

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// A type flagged MultiStep must implement StepChecker (spec 2026-10-03-03),
// and only those types run on the bulk lane.
func TestMultiStepTypesImplementStepChecker(t *testing.T) {
	t.Parallel()

	found := false

	for _, meta := range checkerdef.ListCheckTypeMetas() {
		checker, ok := GetChecker(meta.Type)
		if !ok {
			continue
		}

		_, isStep := checker.(checkerdef.StepChecker)
		if meta.MultiStep {
			found = true

			require.True(t, isStep, "%s is MultiStep but its checker does not implement StepChecker", meta.Type)
			stepChecker, _ := checker.(checkerdef.StepChecker)
			require.Positive(t, stepChecker.UnitsPerSlice(), meta.Type)
		}

		require.Equal(t, meta.MultiStep, meta.Type.IsMultiStep())
	}

	require.True(t, found, "crawl must be registered as a multi-step type")
}
