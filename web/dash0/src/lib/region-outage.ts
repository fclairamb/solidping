import type { RegionDefinition } from "@/api/hooks";

/**
 * Region outages as dash0 sees them (spec 2026-09-25-03).
 *
 * The server's per-minute region sweep holds a cloud region as dark when jobs
 * are assigned to it and no worker is live. `GET /orgs/:org/regions` reports
 * that as `status: "offline"` plus `offlineSince` (the last worker beat).
 * Everything here is derived from that one field: a check is **blind** when
 * every region it runs from is offline (it is not running at all), and
 * **reduced** when only some are (it keeps running from the others).
 *
 * Private (`@`) regions never carry a status — the org's own agent reports on
 * those — so they always count as running here.
 */
export type RegionOutageKind = "blind" | "reduced";

export interface CheckRegionOutage {
  kind: RegionOutageKind;
  /** The check's regions that are offline, in the check's own order. */
  offline: RegionDefinition[];
  /** Earliest `offlineSince` among them. */
  since: string;
  /** How many of the check's regions are still running. */
  liveCount: number;
  /** How many regions the check runs from. */
  totalCount: number;
}

/** True when the regions endpoint reports the region as offline. */
export function isRegionOffline(region: RegionDefinition | undefined): boolean {
  return region?.status === "offline";
}

/** The offline regions of a region list. */
export function offlineRegions(regions: RegionDefinition[] | undefined): RegionDefinition[] {
  return (regions ?? []).filter(isRegionOffline);
}

/**
 * How an outage affects one check, or null when it does not. A check with no
 * regions runs from any region and is never pinned to a dark one; a disabled
 * check has no job at all.
 */
export function checkRegionOutage(
  check: { regions?: string[]; enabled?: boolean },
  regions: RegionDefinition[] | undefined,
): CheckRegionOutage | null {
  if (check.enabled === false) return null;
  const slugs = check.regions ?? [];
  if (slugs.length === 0) return null;

  const offline: RegionDefinition[] = [];
  for (const slug of slugs) {
    const def = regions?.find((r) => r.slug === slug);
    if (def && isRegionOffline(def)) offline.push(def);
  }
  if (offline.length === 0) return null;

  const since = offline
    .map((r) => r.offlineSince ?? "")
    .filter(Boolean)
    .sort()[0] ?? "";

  return {
    kind: offline.length === slugs.length ? "blind" : "reduced",
    offline,
    since,
    liveCount: slugs.length - offline.length,
    totalCount: slugs.length,
  };
}

export interface RegionOutageSummary {
  /** Offline regions at least one listed check depends on. */
  offline: RegionDefinition[];
  since: string;
  blind: { uid: string; name: string }[];
  reduced: { uid: string; name: string }[];
}

/**
 * Summarizes an outage over a list of checks, for the checks list banner.
 * Null when no listed check is affected.
 */
export function summarizeRegionOutage(
  checks: { uid: string; name?: string; slug?: string; regions?: string[]; enabled?: boolean }[],
  regions: RegionDefinition[] | undefined,
): RegionOutageSummary | null {
  const blind: RegionOutageSummary["blind"] = [];
  const reduced: RegionOutageSummary["reduced"] = [];
  const offline = new Map<string, RegionDefinition>();

  for (const check of checks) {
    const outage = checkRegionOutage(check, regions);
    if (!outage) continue;
    const entry = { uid: check.uid, name: check.name || check.slug || check.uid };
    (outage.kind === "blind" ? blind : reduced).push(entry);
    for (const region of outage.offline) offline.set(region.slug, region);
  }

  if (offline.size === 0) return null;

  const list = [...offline.values()];
  const since = list
    .map((r) => r.offlineSince ?? "")
    .filter(Boolean)
    .sort()[0] ?? "";

  return { offline: list, since, blind, reduced };
}

/** "🇫🇷 Lauterbourg, 🇩🇪 Frankfurt" — the offline regions as one label. */
export function offlineRegionNames(regions: RegionDefinition[]): string {
  return regions.map((r) => (r.name ? `${r.emoji ? `${r.emoji} ` : ""}${r.name}` : r.slug)).join(", ");
}
