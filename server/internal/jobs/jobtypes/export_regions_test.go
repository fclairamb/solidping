package jobtypes

import (
	"context"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// ResolveRegionInfoForTest exposes resolveRegionInfo to the external tests.
func ResolveRegionInfoForTest(
	ctx context.Context, jctx *jobdef.JobContext, incident *models.Incident,
) *notifications.RegionInfo {
	return resolveRegionInfo(ctx, jctx, slog.Default(), incident)
}
