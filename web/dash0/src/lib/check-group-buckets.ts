// Group-bucket helpers for the checks index in "groups" mode.
//
// The page loads every check through one infinite stream sorted by
// sort=group: the check's group sortOrder ascending, ungrouped checks last
// (server/internal/db/postgres/postgres.go, groupSortKeyExpr). The stream
// therefore has a *frontier*: once a loaded check belongs to a later group (a
// strictly greater sortOrder, or no group at all), no later page can ever
// deliver a row for an earlier group, so that group's bucket is final.

interface GroupLike {
  uid: string;
  sortOrder: number;
}

interface CheckLike {
  checkGroupUid?: string | null;
}

/**
 * The highest group sort key among the loaded checks: the group's sortOrder,
 * or +Infinity for an ungrouped check (or a check whose group is unknown to
 * the loaded groups list, which the server also sorts last). -Infinity when
 * nothing is loaded yet.
 */
export function streamFrontier(
  checks: Iterable<CheckLike>,
  groups: readonly GroupLike[],
): number {
  const sortOrderByUid = new Map(groups.map((g) => [g.uid, g.sortOrder]));
  let frontier = Number.NEGATIVE_INFINITY;
  for (const check of checks) {
    const key = check.checkGroupUid
      ? (sortOrderByUid.get(check.checkGroupUid) ?? Number.POSITIVE_INFINITY)
      : Number.POSITIVE_INFINITY;
    if (key > frontier) frontier = key;
  }
  return frontier;
}

/**
 * True when no further page of the stream can add a check to `group`: the
 * stream is drained, or it already moved past the group's sortOrder (strictly:
 * groups sharing a sortOrder interleave in the stream).
 */
export function isGroupBucketSettled(
  group: GroupLike,
  frontier: number,
  streaming: boolean,
): boolean {
  return !streaming || frontier > group.sortOrder;
}

/**
 * Whether a group section is rendered. Not filtering, every group stays
 * visible (empty ones with their "No checks" placeholder, so users can find
 * and manage them). Filtering, only groups with at least one matching loaded
 * check: a group whose matches are still on a later page appears when they
 * arrive (it only ever pops in, never out), and an empty one is never shown.
 * Showing still-empty groups as skeletons while filtering pushed the
 * page-level infinite-scroll sentinel out of reach, so the stream stopped and
 * the skeletons stayed forever.
 */
export function isGroupSectionVisible(
  bucketSize: number,
  isFiltering: boolean,
): boolean {
  return !isFiltering || bucketSize > 0;
}
