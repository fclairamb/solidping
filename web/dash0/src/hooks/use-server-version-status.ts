import { useEffect, useState } from "react";
import { useVersion } from "@/api/hooks";
import { getLoadedServerVersion } from "@/lib/server-version";

export interface ServerVersionStatus {
  /** The version snapshot captured once when the page loaded — immutable
   *  for the page's lifetime. `undefined` until the boot fetch resolves. */
  loadedVersion: string | undefined;
  /** The latest version from the 15-minute `/api/mgmt/version` poll. */
  currentVersion: string | undefined;
  /** True once both versions are known and differ — the server was
   *  redeployed after this page loaded. A `"dev"` build compares equal to
   *  itself like any other version, so a dev server never falsely reports
   *  staleness. */
  isStale: boolean;
}

/**
 * Compares the server version this page loaded with against the server's
 * current version, polled in the background (spec 2026-08-28-01).
 *
 * The loaded baseline is fetched once (`getLoadedServerVersion`, outside
 * React Query so nothing can refetch or evict it) and never changes for the
 * life of the page; `currentVersion` moves as `useVersion()` polls every 15
 * minutes and on window focus/`visibilitychange`. Poll failures resolve to
 * `undefined` data and are silent by design — no error state to surface.
 */
export function useServerVersionStatus(): ServerVersionStatus {
  const [loadedVersion, setLoadedVersion] = useState<string | undefined>(undefined);
  const { data } = useVersion();
  const currentVersion = data?.version;

  useEffect(() => {
    let cancelled = false;
    getLoadedServerVersion().then((version) => {
      if (!cancelled) {
        setLoadedVersion(version);
      }
    });
    return () => {
      cancelled = true;
    };
  }, []);

  const isStale =
    loadedVersion !== undefined &&
    currentVersion !== undefined &&
    loadedVersion !== currentVersion;

  return { loadedVersion, currentVersion, isStale };
}
