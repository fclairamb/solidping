// The placement of the last check created in an org, kept in localStorage so
// the next new-check form starts from it (solidping-business#113): someone
// creating a run of checks pinned to the same regions no longer re-picks them
// every time. Per org and per browser; a convenience, never a setting.

import type { CheckPlacement } from "@/api/hooks";

export interface LastPlacement {
  placement: CheckPlacement;
  /** The pinned regions; empty for automatic placement. */
  regions: string[];
  /** The automatic region count; absent for a pinned placement. */
  regionCount?: number;
}

const KEY_PREFIX = "solidping_last_placement:";

function storageKey(org: string): string {
  return `${KEY_PREFIX}${org}`;
}

/** Validates a stored value, dropping anything that isn't a usable placement. */
export function parseLastPlacement(value: unknown): LastPlacement | null {
  if (!value || typeof value !== "object") return null;
  const raw = value as Record<string, unknown>;
  const regions = Array.isArray(raw.regions)
    ? raw.regions.filter((r): r is string => typeof r === "string" && r !== "")
    : [];
  if (raw.placement === "pinned") {
    return regions.length > 0 ? { placement: "pinned", regions } : null;
  }
  if (raw.placement === "auto") {
    const count = raw.regionCount;
    return typeof count === "number" && Number.isInteger(count) && count > 0
      ? { placement: "auto", regions: [], regionCount: count }
      : { placement: "auto", regions: [] };
  }
  return null;
}

export function loadLastPlacement(org: string): LastPlacement | null {
  try {
    const raw = localStorage.getItem(storageKey(org));
    return raw ? parseLastPlacement(JSON.parse(raw)) : null;
  } catch {
    return null;
  }
}

/**
 * Records the placement a check was just created with. A submission that
 * carried no placement (the region picker was hidden) leaves the stored one
 * alone.
 */
export function saveLastPlacement(
  org: string,
  data: { placement?: CheckPlacement; regions?: string[]; regionCount?: number },
): void {
  const value = parseLastPlacement({
    placement: data.placement,
    regions: data.placement === "pinned" ? data.regions : [],
    regionCount: data.regionCount,
  });
  if (!value) return;
  try {
    localStorage.setItem(storageKey(org), JSON.stringify(value));
  } catch {
    // Storage blocked: the form falls back to the org default.
  }
}

/**
 * The stored placement a new-check form should start from, or null to keep
 * its usual defaults: an edit, or a create whose regions were prefilled
 * (`?region=`), never takes it.
 */
export function lastPlacementSeed(
  mode: "create" | "edit",
  prefilledRegions: string[] | undefined,
  last: LastPlacement | null | undefined,
): LastPlacement | null {
  if (mode !== "create" || (prefilledRegions?.length ?? 0) > 0) return null;
  return last ?? null;
}

/**
 * Drops the pinned regions the org no longer offers. A pinned placement left
 * with none falls back to automatic.
 */
export function pruneLastPlacement(
  last: LastPlacement,
  availableSlugs: string[],
): LastPlacement {
  if (last.placement !== "pinned") return last;
  const known = new Set(availableSlugs);
  const regions = last.regions.filter((slug) => known.has(slug));
  return regions.length > 0
    ? { placement: "pinned", regions }
    : { placement: "auto", regions: [] };
}
