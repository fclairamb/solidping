import type { Check, CheckGroup, IncidentDetail } from "@/api/hooks";

/**
 * Read-time aggregation of per-check incidents into check-group buckets
 * (spec 2026-08-24-14).
 *
 * Incidents are per-check now — six members of a "RabbitMQ" group going down
 * produce six incidents, which is what makes each of them independently
 * pageable and lets any of them act as a dependency-rollup parent. The
 * consolidated "RabbitMQ — 2/6 down" view that group incidents used to bake
 * into the row is rebuilt HERE instead, where it is a presentation choice and
 * distorts nothing.
 *
 * Grouping is deliberately client-side: `GET /incidents` stays a flat list
 * with filters (`q`, `limit`, `checkUid`) and no grouping verb, which is the
 * shape every other list endpoint in this API has.
 */

/** One rendered bucket: a group header plus its member incidents, or a single
 * ungrouped incident with no header at all. */
export interface IncidentGroupRow {
  /**
   * Stable key for the bucket — the group uid, or the incident uid when
   * ungrouped.
   */
  key: string;
  /** The check group, when these incidents share one. Absent = render bare. */
  group?: {
    uid: string;
    name: string;
    /** Member incidents in this bucket that are currently active. */
    downCount: number;
    /**
     * Enabled member checks in the group, from the groups endpoint. Undefined
     * when the group is not in the loaded set — render the count alone rather
     * than inventing a denominator.
     */
    memberCount?: number;
  };
  incidents: IncidentDetail[];
}

/** An incident counts toward `downCount` only while it is still open. */
function isActive(incident: IncidentDetail): boolean {
  return incident.state === "active";
}

/**
 * Buckets incidents by the check group their check belongs to.
 *
 * Order is preserved rather than re-sorted: the list arrives in the order the
 * server chose (newest first), and a group takes the position of its FIRST
 * member in that order. Re-sorting would quietly override whatever ordering
 * the caller's filters asked for.
 *
 * Ungrouped incidents keep their own position as single-incident rows with no
 * header, so an org with no check groups renders exactly the flat list it
 * rendered before.
 *
 * @param incidents the incidents currently loaded (one page, not the fleet)
 * @param checks    checks, used only to map checkUid → checkGroupUid
 * @param groups    check groups, for the header name and member count
 */
export function groupIncidentsByCheckGroup(
  incidents: IncidentDetail[] | undefined,
  checks: Check[] | undefined,
  groups: CheckGroup[] | undefined,
): IncidentGroupRow[] {
  if (!incidents || incidents.length === 0) return [];

  const groupOfCheck = new Map<string, string>();
  for (const check of checks ?? []) {
    if (check.uid && check.checkGroupUid) {
      groupOfCheck.set(check.uid, check.checkGroupUid);
    }
  }

  const groupByUid = new Map<string, CheckGroup>();
  for (const group of groups ?? []) {
    groupByUid.set(group.uid, group);
  }

  const rows: IncidentGroupRow[] = [];
  const rowOfGroup = new Map<string, IncidentGroupRow>();

  for (const incident of incidents) {
    const groupUid = incident.checkUid
      ? groupOfCheck.get(incident.checkUid)
      : undefined;

    // No group, or a group we know nothing about: render it bare, in place.
    // A header naming a group we cannot name would be worse than no header.
    if (!groupUid || !groupByUid.has(groupUid)) {
      rows.push({
        key: incident.uid ?? `${incident.checkUid}-${incident.startedAt}`,
        incidents: [incident],
      });

      continue;
    }

    const existing = rowOfGroup.get(groupUid);

    if (existing) {
      existing.incidents.push(incident);

      if (existing.group && isActive(incident)) existing.group.downCount += 1;

      continue;
    }

    const group = groupByUid.get(groupUid)!;
    const row: IncidentGroupRow = {
      key: groupUid,
      group: {
        uid: groupUid,
        name: group.name,
        downCount: isActive(incident) ? 1 : 0,
        memberCount: group.checkCount,
      },
      incidents: [incident],
    };

    rows.push(row);
    rowOfGroup.set(groupUid, row);
  }

  return rows;
}

/**
 * The header's count phrase.
 *
 * With pagination a group's members can straddle a page boundary, so `down`
 * is what is in hand rather than a server-side whole-group total (an accepted
 * v1 trade-off — the payload deliberately grew no group-count field). The
 * "N/M" form is therefore only used when it cannot overstate: as soon as the
 * loaded members exceed the known member count, or the count is unknown, the
 * bare number is the honest phrasing.
 */
export function groupHeaderCounts(row: IncidentGroupRow): {
  down: number;
  total?: number;
} {
  const down = row.group?.downCount ?? 0;
  const total = row.group?.memberCount;

  if (total === undefined || down > total) return { down };

  return { down, total };
}
