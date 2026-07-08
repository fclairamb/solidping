// Live dashboard updates: mounts one WebSocket per org layout and maintains
// a refcounted per-scope subscription registry. Components declare interest
// via useLiveSubscription(scope); the registry sends subscribe/unsubscribe
// on 0<->1 refcount transitions, translates server `update` hints into
// TanStack Query cache invalidations scoped to that subscription, and
// replays every active scope after each reconnect. While a scope is live
// (subscribed and acked), the pages that own it stretch their refetch
// intervals to a lazy safety net — see stretchWhileLive/useScopeLive.
// Disconnected or disabled (feature off / forbidden), everything behaves
// exactly like today's plain polling — graceful degradation is the fallback
// path, not an error path.

import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import {
  connectLiveSocket,
  type LiveEntity,
  type LiveScope,
} from "@/lib/live-socket";

/** Lazy safety-net poll interval used while a scope is live. */
export const LIVE_LAZY_POLL_MS = 5 * 60_000;

/**
 * Returns the effective refetch interval for a polling hook: stretched to
 * the lazy safety net while live, unchanged otherwise.
 */
export function stretchWhileLive(baseMs: number, isLive: boolean): number {
  return isLive ? Math.max(baseMs, LIVE_LAZY_POLL_MS) : baseMs;
}

/** A predicate matching TanStack Query keys to invalidate for one query root. */
type KeyPredicate = (key: unknown[], org: string, uid?: string) => boolean;

interface QueryRoot {
  root: string;
  matches: KeyPredicate;
}

/** key[1] === org — the shape of every org-collection query (checks list,
 * incidents list, events list, jobs stats, …). No uid involved. */
function orgRoot(root: string): QueryRoot {
  return { root, matches: (key, org) => key[0] === root && key[1] === org };
}

/** key[1] === org && key[2] === uid — a per-check detail query (useCheck,
 * useCheckAvailability). */
function checkDetailRoot(root: string): QueryRoot {
  return {
    root,
    matches: (key, org, uid) => key[0] === root && key[1] === org && (!uid || key[2] === uid),
  };
}

/** key[1] === org && key[2]?.checkUid === uid — results/allResults, whose
 * checkUid lives inside an options object, not a bare key segment (a plain
 * key.includes(uid) would never match this shape). */
function resultsRoot(root: string): QueryRoot {
  return {
    root,
    matches: (key, org, uid) => {
      if (key[0] !== root || key[1] !== org) return false;
      if (!uid) return true;
      const opts = key[2] as { checkUid?: string } | undefined;
      return opts?.checkUid === uid;
    },
  };
}

/** Default query roots per (entity, kind). Explicit per the v2 spec — avoids
 * fragile key-predicate guessing, since not every query key holds a uid in
 * the same shape. Entity "check" scopes pass their uid through; collection
 * entities ignore it. */
const DEFAULT_QUERY_ROOTS: Record<LiveEntity, Partial<Record<string, QueryRoot[]>>> = {
  checks: {
    checks: [orgRoot("checks")],
    // A plain result write (no status transition) publishes kind "results"
    // only — never "checks" (see realtime.KindChecks: published separately,
    // only on an actual status transition). The checks list embeds
    // lastResult/lastStatusChange (`with=last_result,last_status_change`),
    // so it must also be invalidated here or "last checked" goes stale for
    // up to the lazy poll interval on every steady-state (no-transition) run.
    results: [
      orgRoot("checks"),
      resultsRoot("results"),
      resultsRoot("allResults"),
      orgRoot("checkAvailability"),
    ],
  },
  check: {
    checks: [checkDetailRoot("check")],
    results: [
      checkDetailRoot("check"),
      resultsRoot("results"),
      resultsRoot("allResults"),
      checkDetailRoot("checkAvailability"),
    ],
    incidents: [checkDetailRoot("check")],
  },
  incidents: {
    incidents: [orgRoot("incidents"), orgRoot("incident")],
  },
  events: {
    events: [orgRoot("events")],
  },
  jobs: {
    jobs: [
      orgRoot("jobsStats"),
      orgRoot("backgroundJobs"),
      orgRoot("backgroundJob"),
      orgRoot("backgroundJobChain"),
      orgRoot("checkSchedule"),
      orgRoot("checkJob"),
    ],
  },
};

function defaultRootsFor(scope: LiveScope, kinds: string[]): QueryRoot[] {
  const byKind = DEFAULT_QUERY_ROOTS[scope.entity] ?? {};
  const roots: QueryRoot[] = [];
  const seen = new Set<string>();
  const effectiveKinds = kinds.length > 0 ? kinds : Object.keys(byKind); // empty kinds = "all"
  for (const kind of effectiveKinds) {
    for (const qr of byKind[kind] ?? []) {
      if (seen.has(qr.root + qr.matches.toString())) continue;
      seen.add(qr.root + qr.matches.toString());
      roots.push(qr);
    }
  }
  return roots;
}

function invalidateScope(
  queryClient: QueryClient,
  org: string,
  scope: LiveScope,
  kinds: string[],
  explicitRoots?: QueryRoot[],
): void {
  const roots = explicitRoots ?? defaultRootsFor(scope, kinds);
  if (roots.length === 0) return;

  void queryClient.invalidateQueries({
    predicate: (query) => {
      const key = query.queryKey;
      if (!Array.isArray(key)) return false;
      return roots.some((qr) => qr.matches(key, org, scope.uid));
    },
  });
}

function scopeKey(scope: LiveScope): string {
  return scope.uid ? `${scope.entity}:${scope.uid}` : scope.entity;
}

/** Per-scope subscription error, set from a server `error` frame and cleared
 * on a successful `subscribed` ack, a fresh subscribe, or a reconnect/drop
 * (see LiveRegistry.onScopeError / onSubscribed / onOpen / markAllScopesNotLive). */
export interface ScopeError {
  code: string;
  title: string;
}

interface ScopeEntry {
  scope: LiveScope;
  count: number;
  queryRoots?: QueryRoot[];
  live: boolean;
  error?: ScopeError;
  listeners: Set<() => void>;
}

/**
 * Connection status for the sidebar indicator dot (see
 * live-status-dot.tsx). Distinct from the per-scope `live` flag:
 * - "connecting" — initial state, or between (re)connect attempts; not an
 *   error (e.g. the API-quiet delay before the first connect).
 * - "live" — `hello` acked, hints streaming.
 * - "reconnecting" — the socket dropped or an attempt failed; retrying with
 *   backoff.
 * - "disabled" — terminal: the socket loop exited (feature off, close 4404,
 *   or access denied, close 4403). Never reported as an error.
 */
export type LiveConnectionStatus = "connecting" | "live" | "reconnecting" | "disabled";

/**
 * LiveRegistry is the external store backing the provider: it owns the
 * socket connection, the refcounted scope map, and per-scope + global live
 * flags. A single instance is created per LiveEventsProvider mount (one per
 * org layout) and exposed through context; hooks subscribe to it via
 * useSyncExternalStore so a scope's own live-flag flip doesn't re-render
 * unrelated consumers.
 */
export class LiveRegistry {
  private scopes = new Map<string, ScopeEntry>();
  private globalListeners = new Set<() => void>();
  private socketHandle: ReturnType<typeof connectLiveSocket> | null = null;
  private status: LiveConnectionStatus = "connecting";

  readonly org: string;

  constructor(
    org: string,
    private queryClient: QueryClient,
    private connect: typeof connectLiveSocket = connectLiveSocket,
  ) {
    this.org = org;
  }

  start(): void {
    this.socketHandle = this.connect(this.org, {
      onOpen: () => {
        this.setStatus("live");
        // Replay every active scope after a (re)connect — the previous
        // socket's subscriptions are gone server-side, and this also covers
        // first connect. Scope-accurate: no more whole-org invalidation.
        for (const entry of this.scopes.values()) {
          this.socketHandle?.send(entry.scope, "subscribe");
        }
      },
      onSubscribed: (scope) => {
        const entry = this.scopes.get(scopeKey(scope));
        if (!entry) return;
        entry.live = true;
        // A successful ack supersedes any earlier rejection for this scope
        // (e.g. a resubscribe after the caller fixed the uid it sent).
        entry.error = undefined;
        this.notifyScope(entry);
        // A fresh subscribe (or a replayed one after reconnect) may have
        // missed updates; invalidate once so the scope catches up.
        invalidateScope(this.queryClient, this.org, scope, [], entry.queryRoots);
      },
      onUpdate: (scope, kinds) => {
        const entry = this.scopes.get(scopeKey(scope));
        invalidateScope(this.queryClient, this.org, scope, kinds, entry?.queryRoots);
      },
      onResync: () => {
        // Bus transport gap: invalidate every currently subscribed scope once.
        for (const entry of this.scopes.values()) {
          invalidateScope(this.queryClient, this.org, entry.scope, [], entry.queryRoots);
        }
      },
      onScopeError: (scope, error) => {
        const entry = this.scopes.get(scopeKey(scope));
        if (!entry) return;
        entry.error = error;
        this.notifyScope(entry);
      },
      onDisconnected: () => {
        this.setStatus("reconnecting");
        this.markAllScopesNotLive();
      },
      onDisabled: () => {
        this.setStatus("disabled");
        this.markAllScopesNotLive();
      },
    });
  }

  stop(): void {
    this.socketHandle?.disconnect();
    this.socketHandle = null;
  }

  addScope(scope: LiveScope, queryRoots?: QueryRoot[]): void {
    const key = scopeKey(scope);
    let entry = this.scopes.get(key);
    if (!entry) {
      entry = { scope, count: 0, queryRoots, live: false, listeners: new Set() };
      this.scopes.set(key, entry);
    }
    entry.count += 1;
    if (entry.count === 1) {
      this.socketHandle?.send(scope, "subscribe");
    }
  }

  removeScope(scope: LiveScope): void {
    const key = scopeKey(scope);
    const entry = this.scopes.get(key);
    if (!entry) return;
    entry.count -= 1;
    if (entry.count <= 0) {
      this.scopes.delete(key);
      this.socketHandle?.send(scope, "unsubscribe");
    }
  }

  isScopeLive(scope: LiveScope): boolean {
    return this.scopes.get(scopeKey(scope))?.live ?? false;
  }

  getScopeError(scope: LiveScope): ScopeError | undefined {
    return this.scopes.get(scopeKey(scope))?.error;
  }

  subscribeScope(scope: LiveScope, listener: () => void): () => void {
    const key = scopeKey(scope);
    // The entry may not exist yet the first render (addScope runs in an
    // effect, which fires after this hook's first render) — lazily track
    // listeners on a placeholder so a later addScope can find them.
    let entry = this.scopes.get(key);
    if (!entry) {
      entry = { scope, count: 0, live: false, listeners: new Set() };
      this.scopes.set(key, entry);
    }
    entry.listeners.add(listener);
    return () => {
      entry?.listeners.delete(listener);
    };
  }

  subscribeGlobal(listener: () => void): () => void {
    this.globalListeners.add(listener);
    return () => {
      this.globalListeners.delete(listener);
    };
  }

  /** Derived from status: true only while `"live"`. Keeps
   * useLiveStatus/useScopeLive and poll stretching semantics unchanged. */
  getGlobalLive = (): boolean => this.status === "live";

  getStatus = (): LiveConnectionStatus => this.status;

  getScopeLiveSnapshot(scope: LiveScope): () => boolean {
    return () => this.isScopeLive(scope);
  }

  private setStatus(value: LiveConnectionStatus): void {
    // "disabled" is terminal (the socket loop has exited) — never let a
    // stray/late notification bounce it back to reconnecting or live. In
    // practice connectLiveSocket's run() loop already guarantees this (it
    // returns immediately after onDisabled), but pinning it here too means
    // the state machine's own "terminal" contract can't be violated by a
    // future caller.
    if (this.status === "disabled") return;
    if (this.status === value) return;
    this.status = value;
    for (const listener of this.globalListeners) listener();
  }

  private markAllScopesNotLive(): void {
    for (const entry of this.scopes.values()) {
      // A drop/disable invalidates any pending subscribe result — clear a
      // stale error too, so a transient rejection doesn't keep showing the
      // degraded-mode indicator through an unrelated disconnect/reconnect
      // cycle; the upcoming reconnect's replay will surface a fresh error if
      // the same scope is still invalid server-side.
      const changed = entry.live || entry.error !== undefined;
      entry.live = false;
      entry.error = undefined;
      if (changed) {
        this.notifyScope(entry);
      }
    }
  }

  private notifyScope(entry: ScopeEntry): void {
    for (const listener of entry.listeners) listener();
  }
}

const LiveRegistryContext = createContext<LiveRegistry | null>(null);

export function LiveEventsProvider({
  org,
  children,
}: {
  org: string;
  children: ReactNode;
}) {
  const queryClient = useQueryClient();
  // Recreated on org change (route to a different org remounts the layout
  // in practice, but useMemo guards a live org swap regardless) — a plain
  // ref mutated during render is disallowed by the React Compiler lint
  // rule, and this is exactly the "derive once per key" case useMemo covers.
  const registry = useMemo(() => new LiveRegistry(org, queryClient), [org, queryClient]);

  useEffect(() => {
    if (!org) return undefined;

    registry.start();
    return () => registry.stop();
  }, [org, registry]);

  return (
    <LiveRegistryContext.Provider value={registry}>{children}</LiveRegistryContext.Provider>
  );
}

function useRegistry(): LiveRegistry | null {
  return useContext(LiveRegistryContext);
}

/**
 * Reports whether the live socket is streaming (global "socket open"
 * signal). Outside the provider, disconnected, or disabled, this is always
 * false, so consumers keep today's polling behavior. Coarse — prefer
 * useScopeLive for gating a specific page's poll stretching.
 */
export function useLiveStatus(): { isLive: boolean } {
  const registry = useRegistry();
  const isLive = useSyncExternalStore(
    (listener) => registry?.subscribeGlobal(listener) ?? (() => {}),
    () => registry?.getGlobalLive() ?? false,
    () => false,
  );
  return { isLive };
}

/**
 * The four-state connection status for the sidebar indicator dot (see
 * live-status-dot.tsx). Outside the provider, always reports "connecting"
 * — the dot simply isn't rendered there (login/register have no sidebar).
 */
export function useLiveConnectionStatus(): LiveConnectionStatus {
  const registry = useRegistry();
  return useSyncExternalStore(
    (listener) => registry?.subscribeGlobal(listener) ?? (() => {}),
    () => registry?.getStatus() ?? "connecting",
    () => "connecting",
  );
}

/**
 * Declares interest in a scope for the lifetime of the calling component:
 * subscribes on mount (0->1 refcount sends `subscribe`), unsubscribes on
 * unmount (1->0 sends `unsubscribe`). Pass explicit queryRoots to override
 * the default kind->root mapping (rarely needed — the defaults cover every
 * shipped query key shape); most callers omit it.
 *
 * Multiple components may subscribe to the same scope concurrently (e.g. two
 * widgets both watching `checks`) — the registry refcounts so the
 * unsubscribe only reaches the server once the last interested component
 * unmounts.
 *
 * Pass `undefined` to disable the subscription entirely — a no-op, no
 * `subscribe`/`unsubscribe` frame is ever sent. This is how a caller gates a
 * per-uid scope until the uid is actually known (e.g. the check detail page
 * waiting for its REST fetch to resolve the canonical uid): hooks can't be
 * called conditionally, so the disabled state has to be a value, not a
 * skipped call.
 */
export function useLiveSubscription(scope: LiveScope | undefined): void {
  const registry = useRegistry();
  const entityKey = scope?.entity;
  const uidKey = scope?.uid;

  useEffect(() => {
    if (!registry || !entityKey) return undefined;
    const s: LiveScope = uidKey ? { entity: entityKey, uid: uidKey } : { entity: entityKey };
    registry.addScope(s);
    return () => registry.removeScope(s);
  }, [registry, entityKey, uidKey]);
}

/**
 * Per-scope liveness: true only between that scope's `subscribed` ack and
 * disconnect. A rejected or not-yet-acked subscription reports false, so the
 * page keeps polling at its base rate until the ack lands — gate
 * stretchWhileLive on this, not the coarse useLiveStatus, for scope-accurate
 * poll stretching.
 *
 * `undefined` (disabled scope, uid not yet known) always reports false, same
 * as "not live yet" — the caller keeps polling at its base rate.
 */
export function useScopeLive(scope: LiveScope | undefined): boolean {
  const registry = useRegistry();
  const entityKey = scope?.entity;
  const uidKey = scope?.uid;

  return useSyncExternalStore(
    (listener) => {
      if (!registry || !entityKey) return () => {};
      const s: LiveScope = uidKey ? { entity: entityKey, uid: uidKey } : { entity: entityKey };
      return registry.subscribeScope(s, listener);
    },
    () => {
      if (!registry || !entityKey) return false;
      const s: LiveScope = uidKey ? { entity: entityKey, uid: uidKey } : { entity: entityKey };
      return registry.isScopeLive(s);
    },
    () => false,
  );
}

/**
 * Per-scope subscription error: set from a server `error` frame (NOT_FOUND,
 * VALIDATION_ERROR, CONCURRENCY_LIMITED, INTERNAL_ERROR — see
 * realtimews.handleSubscribe), cleared on a successful `subscribed` ack or a
 * reconnect/drop. Pair with a visible, non-blocking degraded-mode indicator
 * in the consuming page — polling keeps working regardless, this is purely
 * informational (mirrors useScopeLive's "gate on ack" pattern, but for the
 * rejection path instead of the success path).
 *
 * `undefined` scope (uid not yet known) always reports no error.
 */
export function useScopeError(scope: LiveScope | undefined): ScopeError | undefined {
  const registry = useRegistry();
  const entityKey = scope?.entity;
  const uidKey = scope?.uid;

  return useSyncExternalStore(
    (listener) => {
      if (!registry || !entityKey) return () => {};
      const s: LiveScope = uidKey ? { entity: entityKey, uid: uidKey } : { entity: entityKey };
      return registry.subscribeScope(s, listener);
    },
    () => {
      if (!registry || !entityKey) return undefined;
      const s: LiveScope = uidKey ? { entity: entityKey, uid: uidKey } : { entity: entityKey };
      return registry.getScopeError(s);
    },
    () => undefined,
  );
}
