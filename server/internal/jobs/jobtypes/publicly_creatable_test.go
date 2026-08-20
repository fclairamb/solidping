package jobtypes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// publiclyCreatableTypes is the allowlist this test pins, spelled out
// independently of jobdef's own literal so a change there has to be made
// twice — deliberately, with this test as the second signature.
//
//nolint:gochecknoglobals // test fixture, treated as a constant.
var publiclyCreatableTypes = map[jobdef.JobType]bool{
	jobdef.JobTypeSleep: true,
}

// TestOnlyAllowlistedTypesArePubliclyCreatable walks the WHOLE registry and
// asserts every type is closed to the public create endpoint except the
// allowlisted ones. Adding a job type can therefore never quietly open it, and
// opting one in fails here until someone updates this list on purpose
// (spec 2026-08-15-04).
func TestOnlyAllowlistedTypesArePubliclyCreatable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.NotEmpty(jobDefinitionFactories, "the registry must not be empty, or this test proves nothing")

	for jobType := range jobDefinitionFactories {
		want := publiclyCreatableTypes[jobType]
		r.Equal(want, jobdef.IsPubliclyCreatable(jobType),
			"job type %q: publicly creatable must be %v — if you are opting a type in, "+
				"read jobdef.publiclyCreatableJobTypes first", jobType, want)
	}

	// Every allowlisted type must actually exist in the registry, otherwise the
	// allowlist is silently naming a type nobody can create anyway.
	for jobType := range publiclyCreatableTypes {
		_, ok := GetJobDefinition(jobType)
		r.True(ok, "allowlisted job type %q is not registered", jobType)
	}

	// The dangerous ones, named explicitly: arbitrary mail and SSRF.
	r.False(jobdef.IsPubliclyCreatable(jobdef.JobTypeEmail))
	r.False(jobdef.IsPubliclyCreatable(jobdef.JobTypeWebhook))
	// The uptime report fans out mail to stored addresses, so it is arbitrary
	// mail with extra steps (spec 2026-08-20-01).
	r.False(jobdef.IsPubliclyCreatable(jobdef.JobTypeUptimeReport))

	// A type that does not exist at all is closed too (deny by default).
	r.False(jobdef.IsPubliclyCreatable(jobdef.JobType("nonexistent")))
}
