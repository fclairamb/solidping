package backend_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestSubmitResultCapturesDNSBaseline pins spec 2026-10-03-04 on the
// in-process path: a dns result carrying baseline_capture stores the region's
// baseline on the check and its job.
func TestSubmitResultCapturesDNSBaseline(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	be, _, dbSvc, ctx := newDirectBackend(t)

	org := models.NewOrganization("submit-baseline", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "dns-ns", "dns")
	check.Config = models.JSONMap{"host": "acme.com", "record_type": "NS", "detect_changes": true}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.NotEmpty(jobs)

	job := jobs[0]
	job.Check = check

	region := "eu"
	req := submitReq()
	req.Region = &region
	req.Output = map[string]any{"baseline_capture": []string{"ns1.acme.com"}}

	_ = be.SubmitResult(ctx, job, registerWorker(ctx, t, dbSvc, "wk-baseline"), req)

	stored, err := dbSvc.GetCheck(ctx, org.UID, check.UID)
	r.NoError(err)

	baseline, ok := stored.Config["baseline"].(map[string]any)
	r.True(ok, "baseline must be stored, got %v", stored.Config)
	r.Equal([]any{"ns1.acme.com"}, baseline["eu"])
}
