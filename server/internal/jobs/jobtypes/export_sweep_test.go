package jobtypes

import (
	"sort"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// RegisteredJobTypes lists every registered job type, sorted.
func RegisteredJobTypes() []jobdef.JobType {
	types := make([]jobdef.JobType, 0, len(jobDefinitionFactories))
	for jobType := range jobDefinitionFactories {
		types = append(types, jobType)
	}

	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })

	return types
}
