// Client-side twin of models.RollupGroupStatus
// (server/internal/db/models/check_group_status.go), for sections that have no
// server-side identity (the checks list's host buckets). Worst-of rank, spec
// 2026-09-25-02: down > validating > warning > stale > up.
//
//   - every considered member down        → down
//   - some (not all) down                 → degraded
//   - otherwise any validating            → validating
//   - otherwise any warning               → warning
//   - otherwise any stale ("No data")     → stale — an all-stale section reads
//                                            stale, never created
//   - otherwise any up                    → up
//   - nothing counted                     → created
export function rollupSectionStatus(counts: Record<string, number>): string {
  const total = Object.values(counts).reduce((sum, n) => sum + n, 0);
  if (total === 0) return "created";

  const down = counts.down ?? 0;
  if (down === total) return "down";
  if (down > 0) return "degraded";
  if ((counts.validating ?? 0) > 0) return "validating";
  if ((counts.warning ?? 0) > 0) return "warning";
  if ((counts.stale ?? 0) > 0) return "stale";
  if ((counts.up ?? 0) > 0) return "up";
  return "created";
}
