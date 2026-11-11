/**
 * GHSA-c9qq-4m99-c7f6 (spec 2026-11-11-05): on v0.47.0, a region key
 * generated in dash0 could reach PostHog through session replay. A shared
 * region key created before the fix gets a one-time notice asking the admin
 * to rotate it.
 *
 * The cutoff is a few days past the fix commit (2026-11-11) so it covers
 * instances that upgrade a little later. A key created after it was generated
 * by a fixed dashboard. A false positive only shows a dismissible notice.
 */
export const REGION_KEY_ADVISORY_CUTOFF = "2026-11-18T00:00:00Z";

const DISMISSED_PREFIX = "sp.regionKeyAdvisory.dismissed.";

/** Whether a region key created at `createdAt` predates the fix. */
export function regionKeyPredatesFix(createdAt: string | undefined | null): boolean {
  if (!createdAt) return false;
  const created = Date.parse(createdAt);
  return Number.isFinite(created) && created < Date.parse(REGION_KEY_ADVISORY_CUTOFF);
}

function storage(): Storage | null {
  try {
    return typeof window === "undefined" ? null : window.localStorage;
  } catch {
    return null;
  }
}

/** Whether the notice was dismissed for this key (by its fingerprint). */
export function isRegionKeyAdvisoryDismissed(fingerprint: string): boolean {
  return storage()?.getItem(DISMISSED_PREFIX + fingerprint) === "1";
}

/** Remembers, in this browser, that the notice was dismissed for this key. */
export function dismissRegionKeyAdvisory(fingerprint: string): void {
  try {
    storage()?.setItem(DISMISSED_PREFIX + fingerprint, "1");
  } catch {
    // Storage full or blocked: the notice comes back next visit, harmless.
  }
}
