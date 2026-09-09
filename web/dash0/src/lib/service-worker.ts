/**
 * Service-worker registration, including the one-time migration off the
 * dashboard's retired `/dash0/` base path (spec 2026-09-09-01).
 *
 * Why a migration is needed at all: a push subscription is bound to a service
 * worker *registration*, and a registration is keyed by its scope. The worker
 * used to be registered from `/dash0/sw.js`, so its scope was `/dash0/`. A page
 * at `/d/…` is not inside that scope, so `navigator.serviceWorker.ready` — what
 * `useWebPushSubscription` waits on — never resolves against it, and the old
 * registration would sit there forever receiving pushes for a URL space the app
 * no longer uses.
 *
 * The `301` that `/dash0/*` now answers does NOT fix this: browsers refuse a
 * service-worker script that responds with a redirect, so the old worker is
 * never updated — it is silently kept. It has to be unregistered explicitly.
 */

/**
 * LEGACY_SW_SCOPE_PATHS are the scopes of registrations this app used to
 * create, which must be torn down on boot. Scope pathnames, with the trailing
 * slash the browser normalizes them to.
 */
export const LEGACY_SW_SCOPE_PATHS = ["/dash0/"];

/**
 * ORPHANED_PUSH_ENDPOINTS_KEY holds the push endpoints that belonged to a
 * legacy registration we tore down. Unregistering invalidates the subscription
 * in the browser, but the server still has a row keyed by that endpoint
 * (`notifications/webpush.go` stores and prunes subscriptions by endpoint URL),
 * so the endpoint is remembered until the notifications page can swap the row
 * for a fresh subscription — replacing it rather than leaving a duplicate.
 */
export const ORPHANED_PUSH_ENDPOINTS_KEY = "solidping.webpush.orphanedEndpoints";

function readStash(): string[] {
  try {
    const raw = window.localStorage.getItem(ORPHANED_PUSH_ENDPOINTS_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed)
      ? parsed.filter((e): e is string => typeof e === "string")
      : [];
  } catch {
    return [];
  }
}

function writeStash(endpoints: string[]): void {
  try {
    if (endpoints.length === 0) {
      window.localStorage.removeItem(ORPHANED_PUSH_ENDPOINTS_KEY);
      return;
    }
    window.localStorage.setItem(
      ORPHANED_PUSH_ENDPOINTS_KEY,
      JSON.stringify([...new Set(endpoints)]),
    );
  } catch {
    // Private mode / storage disabled: the migration still unregisters the
    // stale worker, it just cannot offer to re-subscribe later.
  }
}

/** peekOrphanedPushEndpoints reads the stash without clearing it. */
export function peekOrphanedPushEndpoints(): string[] {
  return readStash();
}

/** takeOrphanedPushEndpoints reads the stash and clears it. */
export function takeOrphanedPushEndpoints(): string[] {
  const endpoints = readStash();
  writeStash([]);
  return endpoints;
}

/** forgetOrphanedPushEndpoint drops one endpoint from the stash. */
export function forgetOrphanedPushEndpoint(endpoint: string): void {
  writeStash(readStash().filter((e) => e !== endpoint));
}

/**
 * isLegacyScope reports whether a registration scope belongs to a retired base
 * path. Compared on the pathname, so it is origin-independent.
 */
export function isLegacyScope(scope: string, origin = "http://localhost"): boolean {
  try {
    return LEGACY_SW_SCOPE_PATHS.includes(new URL(scope, origin).pathname);
  } catch {
    return false;
  }
}

/**
 * unregisterLegacyServiceWorkers tears down every registration scoped to a
 * retired base path, remembering any live push endpoint first so the
 * server-side row can be replaced later.
 *
 * Returns the scopes it removed.
 */
export async function unregisterLegacyServiceWorkers(): Promise<string[]> {
  const removed: string[] = [];

  const registrations = await navigator.serviceWorker.getRegistrations();

  for (const registration of registrations) {
    if (!isLegacyScope(registration.scope, window.location.origin)) continue;

    // Read the subscription BEFORE unregistering: unregister() invalidates it,
    // and the endpoint is the only handle on the now-stale server-side row.
    try {
      const subscription = await registration.pushManager.getSubscription();
      if (subscription) {
        writeStash([...readStash(), subscription.endpoint]);
      }
    } catch {
      // A browser that denies pushManager access here still gets the stale
      // registration removed, which is the part that matters.
    }

    if (await registration.unregister()) {
      removed.push(registration.scope);
    }
  }

  return removed;
}

/**
 * registerServiceWorker migrates off any retired scope and then registers the
 * worker for the current base path. Safe to call unconditionally; it no-ops in
 * a browser without service-worker support.
 */
export async function registerServiceWorker(
  base: string = import.meta.env.BASE_URL || "/",
): Promise<ServiceWorkerRegistration | null> {
  if (typeof navigator === "undefined" || !("serviceWorker" in navigator)) {
    return null;
  }

  const scope = base.endsWith("/") ? base : base + "/";

  try {
    await unregisterLegacyServiceWorkers();

    return await navigator.serviceWorker.register(`${scope}sw.js`, { scope });
  } catch (err) {
    console.warn("[solidping] SW registration failed", err);
    return null;
  }
}
