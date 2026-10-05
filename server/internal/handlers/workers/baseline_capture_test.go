package workers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
)

// TestSubmitResultCapturesDNSBaseline pins spec 2026-10-03-04 on the agent
// path: a dns result submitted over the remote transport (Output decoded from
// JSON, so the capture is a []any) stores the region's baseline exactly like
// the in-process path does.
func TestSubmitResultCapturesDNSBaseline(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newSubmitEnv(t)
	ctx := t.Context()

	check := models.NewCheck(e.org.UID, "dns-ns", "dns")
	check.Config = models.JSONMap{"host": "acme.com", "record_type": "NS", "detect_changes": true}
	check.Regions = []string{"eu-west-1"}
	r.NoError(e.dbSvc.CreateCheck(ctx, check))

	var job models.CheckJob
	r.NoError(e.dbSvc.DB().NewSelect().Model(&job).Where("check_uid = ?", check.UID).Scan(ctx))

	_, err := e.dbSvc.DB().NewUpdate().Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = ?", e.workerUID).
		Set("lease_expires_at = ?", time.Now().Add(time.Minute)).
		Where("uid = ?", job.UID).Exec(ctx)
	r.NoError(err)

	var output map[string]any
	r.NoError(json.Unmarshal([]byte(`{"baseline_capture":["ns1.acme.com","ns2.acme.com"]}`), &output))

	_, err = e.svc.SubmitResult(ctx, &workers.SubmitResultRequest{
		JobUID:    job.UID,
		WorkerUID: e.workerUID,
		Status:    int(models.ResultStatusUp),
		Duration:  12,
		Output:    output,
		FromProbe: true,
	})
	r.NoError(err)

	stored, err := e.dbSvc.GetCheck(ctx, e.org.UID, check.UID)
	r.NoError(err)

	baseline, ok := stored.Config["baseline"].(map[string]any)
	r.True(ok, "baseline must be stored, got %v", stored.Config)
	r.Equal([]any{"ns1.acme.com", "ns2.acme.com"}, baseline["eu-west-1"])

	reloaded := e.reload(job.UID)
	jobBaseline, ok := reloaded.Config["baseline"].(map[string]any)
	r.True(ok)
	r.Equal(baseline, jobBaseline)
}
