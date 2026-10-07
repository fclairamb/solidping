package jobtypes_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

type recordingRepairer struct {
	orgUID, checkUID string
}

func (r *recordingRepairer) RepairCheck(_ context.Context, orgUID, checkUID string) error {
	r.orgUID, r.checkUID = orgUID, checkUID

	return nil
}

func TestAIRepairJobCallsTheRepairer(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	def, ok := jobtypes.GetJobDefinition(jobdef.JobTypeAIRepair)
	r.True(ok)

	_, err := def.CreateJobRun(json.RawMessage(`{}`))
	r.Error(err, "a check is required")

	run, err := def.CreateJobRun(json.RawMessage(`{"checkUid":"chk-1"}`))
	r.NoError(err)

	// No repairer (feature off): a no-op.
	orgUID := "org-1"
	r.NoError(run.Run(t.Context(), &jobdef.JobContext{OrganizationUID: &orgUID, Services: services.NewRegistry()}))

	repairer := &recordingRepairer{}
	registry := services.NewRegistry()
	registry.AIRepair = repairer
	r.NoError(run.Run(t.Context(), &jobdef.JobContext{OrganizationUID: &orgUID, Services: registry}))
	r.Equal("org-1", repairer.orgUID)
	r.Equal("chk-1", repairer.checkUID)
}
