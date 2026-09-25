import type { FailQuorum, RegionFreshness } from "@/api/hooks";

// Multi-region quorum (spec 2026-09-25-10): a client-side mirror of the
// server's resolution (server/internal/regionquorum), used by the check form to
// say what the setting means for the regions being picked. The server has the
// last word: the check detail shows its `effectiveFailQuorum` and
// `regionalIssue`, never a value derived here.

/** The form's four choices. */
export type FailQuorumMode = "default" | "all" | "majority" | "count";

/** Region count from which the default switches from "all" to "majority". */
const MAJORITY_FROM = 3;

/** Bounds of an explicit count, as the server enforces them. */
export const FAIL_QUORUM_MIN = 1;
export const FAIL_QUORUM_MAX = 100;

/** Splits a stored value into the form's mode and count. */
export function failQuorumMode(value: FailQuorum | undefined): {
  mode: FailQuorumMode;
  count: string;
} {
  if (typeof value === "number") {
    return { mode: "count", count: String(value) };
  }

  if (value === "all" || value === "majority") {
    return { mode: value, count: "" };
  }

  return { mode: "default", count: "" };
}

/**
 * The value to send, or undefined when the count is not a whole number in
 * range (the form shows an error then, and the server would refuse it).
 */
export function failQuorumValue(mode: FailQuorumMode, count: string): FailQuorum | undefined {
  if (mode !== "count") {
    return mode;
  }

  const trimmed = count.trim();
  if (!/^\d+$/.test(trimmed)) {
    return undefined;
  }

  const parsed = Number(trimmed);
  if (parsed < FAIL_QUORUM_MIN || parsed > FAIL_QUORUM_MAX) {
    return undefined;
  }

  return parsed;
}

/** How many of `regions` must be failing for the check to be down. 0 when there is no region. */
export function resolveFailQuorum(value: FailQuorum | undefined, regions: number): number {
  if (regions <= 0) {
    return 0;
  }

  if (typeof value === "number") {
    return Math.min(Math.max(value, 1), regions);
  }

  switch (value) {
    case "all":
      return regions;
    case "majority":
      return Math.floor(regions / 2) + 1;
    default:
      return regions < MAJORITY_FROM ? regions : Math.floor(regions / 2) + 1;
  }
}

/** A region reading counts as failing: down, timeout or error. */
export function isFailingRegionStatus(status: RegionFreshness["status"]): boolean {
  return status === "down" || status === "timeout" || status === "error";
}
