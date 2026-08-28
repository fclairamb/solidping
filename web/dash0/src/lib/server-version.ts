import { apiFetch } from "@/api/client";

interface MgmtVersionResponse {
  version?: string;
}

let loadedVersionPromise: Promise<string | undefined> | undefined;

/**
 * The version of the server that served this page — captured ONCE, from the
 * first `/api/mgmt/version` response, and cached for the rest of the page's
 * lifetime (spec 2026-08-28-01).
 *
 * dash0 is embedded in the Go binary, so this boot-time snapshot *is* the
 * version of the JS bundle currently running: a later poll (`useVersion`)
 * that returns a different value means the server was redeployed after this
 * page loaded. That comparison only works if this baseline never moves, so
 * it is deliberately NOT routed through React Query — a query result can be
 * refetched or evicted from cache on remount; this module-level promise
 * cannot be either. Concurrent callers before the first response resolves
 * share the same in-flight request rather than firing duplicates.
 */
export function getLoadedServerVersion(): Promise<string | undefined> {
  loadedVersionPromise ??= apiFetch<MgmtVersionResponse>("/api/mgmt/version", {
    skipAuth: true,
  })
    .then((data) => data.version)
    .catch(() => undefined);

  return loadedVersionPromise;
}

/** Test-only: clears the cached baseline so each test starts from a clean slate. */
export function resetLoadedServerVersionForTests(): void {
  loadedVersionPromise = undefined;
}
