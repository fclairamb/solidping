package jobtypes

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// The worker looks a definition up by the type stored on the job row, then
// hands it the row's config. Two things must hold for every registered type:
// the definition announces the type it is registered under, and a config that
// is not valid JSON is refused before any runner exists.
func TestRegistry_EveryDefinitionKeepsItsContract(t *testing.T) {
	t.Parallel()

	types := RegisteredJobTypes()
	require.NotEmpty(t, types)

	for _, jobType := range types {
		t.Run(string(jobType), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			def, ok := GetJobDefinition(jobType)
			r.True(ok)
			r.Equal(jobType, def.Type())

			for _, malformed := range []string{`{"`, `[1]`} {
				runner, err := def.CreateJobRun(json.RawMessage(malformed))
				r.Error(err, "config %s must be refused", malformed)
				r.Nil(runner)
			}
		})
	}
}

func TestRegistry_UnknownType(t *testing.T) {
	t.Parallel()

	def, ok := GetJobDefinition(jobdef.JobType("no-such-job"))
	require.False(t, ok)
	require.Nil(t, def)
}
