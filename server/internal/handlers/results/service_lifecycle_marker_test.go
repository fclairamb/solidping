package results

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestGetResult_LifecycleMarkerRowNoFallback covers spec 2026-07-08-04: once
// the aggregation job stops deleting lifecycle-marker rows (created,
// running), GetResult must resolve them directly via the "direct" path
// (service.go ~442-454) with no Fallback set — the row is never rolled up in
// the first place, so it must never hit the UUIDv7 covering-aggregation
// fallback path.
func TestGetResult_LifecycleMarkerRowNoFallback(t *testing.T) {
	t.Parallel()

	dbSvc, ctx := newNeighborsTestDB(t)
	r := require.New(t)
	svc := NewService(dbSvc, nil)

	org := models.NewOrganization("lifecycle-marker-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := newBareCheck(ctx, dbSvc, r, org.UID, "lifecycle-marker-check")

	createdRow := models.NewResult(org.UID, check.UID, models.ResultStatusCreated, 0)
	createdRow.Duration = nil
	r.NoError(dbSvc.CreateResult(ctx, createdRow))

	runningRow := models.NewResult(org.UID, check.UID, models.ResultStatusRunning, 0)
	runningRow.Duration = nil
	r.NoError(dbSvc.CreateResult(ctx, runningRow))

	t.Run("created", func(t *testing.T) {
		t.Parallel()

		resp, err := svc.GetResult(ctx, org.Slug, check.UID, createdRow.UID, nil)
		r.NoError(err)
		r.Nil(resp.Fallback, "kept lifecycle row must resolve directly, no fallback banner")
		r.Equal(createdRow.UID, resp.UID)
		r.Equal(statusStrCreated, resp.Status)
		r.Equal(models.PeriodTypeRaw, resp.PeriodType)
	})

	t.Run("running", func(t *testing.T) {
		t.Parallel()

		resp, err := svc.GetResult(ctx, org.Slug, check.UID, runningRow.UID, nil)
		r.NoError(err)
		r.Nil(resp.Fallback, "kept lifecycle row must resolve directly, no fallback banner")
		r.Equal(runningRow.UID, resp.UID)
		r.Equal(statusStrRunning, resp.Status)
		r.Equal(models.PeriodTypeRaw, resp.PeriodType)
	})
}
