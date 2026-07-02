// Live dashboard updates: mounts one SSE hint stream per org layout and
// translates data-free hints into TanStack Query cache invalidations. While
// the stream is connected, polling hooks stretch their refetch intervals to a
// lazy safety net (see useLiveStatus / stretchWhileLive); when disconnected
// or the feature is disabled server-side, everything behaves exactly as
// before — graceful degradation is the fallback path, not an error path.

import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { connectLiveEvents } from "@/lib/live-events";

/** Lazy safety-net poll interval used while the live stream is connected. */
export const LIVE_LAZY_POLL_MS = 5 * 60_000;

/**
 * Maps a server hint kind to the query-key roots it invalidates. Matching is
 * root + org-in-key so variants like ["checks","infinite",org] are covered.
 */
export const HINT_KIND_QUERY_ROOTS: Record<string, string[]> = {
  checks: ["checks", "check"],
  results: ["results", "allResults", "result", "checkAvailability"],
  incidents: ["incidents", "incident"],
  events: ["events"],
  jobs: [
    "jobsStats",
    "backgroundJobs",
    "backgroundJob",
    "backgroundJobChain",
    "checkSchedule",
    "checkJob",
  ],
};

/** Debounce window for merging bursts of hints into one invalidation pass. */
const HINT_DEBOUNCE_MS = 300;

export function invalidateForKinds(
  queryClient: QueryClient,
  org: string,
  kinds: string[],
): void {
  const roots = new Set<string>();
  for (const kind of kinds) {
    for (const root of HINT_KIND_QUERY_ROOTS[kind] ?? []) {
      roots.add(root);
    }
  }
  if (roots.size === 0) return;

  void queryClient.invalidateQueries({
    predicate: (query) => {
      const key = query.queryKey;
      return (
        Array.isArray(key) &&
        typeof key[0] === "string" &&
        roots.has(key[0]) &&
        key.includes(org)
      );
    },
  });
}

/** Full resync: invalidate every org-scoped query once. */
export function invalidateAllForOrg(queryClient: QueryClient, org: string): void {
  void queryClient.invalidateQueries({
    predicate: (query) =>
      Array.isArray(query.queryKey) && query.queryKey.includes(org),
  });
}

interface LiveEventsState {
  /** True while the hint stream is connected. */
  isLive: boolean;
}

const LiveEventsContext = createContext<LiveEventsState>({ isLive: false });

/**
 * Reports whether live updates are streaming. Outside the provider (or when
 * the stream is down / the feature is disabled) this is always false, so
 * consumers keep today's polling behavior.
 */
export function useLiveStatus(): LiveEventsState {
  return useContext(LiveEventsContext);
}

/**
 * Returns the effective refetch interval for a polling hook: stretched to
 * the lazy safety net while live, unchanged otherwise.
 */
export function stretchWhileLive(
  baseMs: number,
  isLive: boolean,
): number {
  return isLive ? Math.max(baseMs, LIVE_LAZY_POLL_MS) : baseMs;
}

export function LiveEventsProvider({
  org,
  children,
}: {
  org: string;
  children: ReactNode;
}) {
  const [isLive, setIsLive] = useState(false);
  const queryClient = useQueryClient();

  useEffect(() => {
    if (!org) return undefined;

    let pendingKinds = new Set<string>();
    let debounceTimer: ReturnType<typeof setTimeout> | null = null;

    const flushHints = () => {
      debounceTimer = null;
      const kinds = Array.from(pendingKinds);
      pendingKinds = new Set();
      invalidateForKinds(queryClient, org, kinds);
    };

    const disconnect = connectLiveEvents(org, {
      onConnected: () => {
        setIsLive(true);
        // One full refresh per (re)connect: anything that changed while the
        // stream was down is picked up now.
        invalidateAllForOrg(queryClient, org);
      },
      onHint: (kinds) => {
        for (const kind of kinds) pendingKinds.add(kind);
        debounceTimer ??= setTimeout(flushHints, HINT_DEBOUNCE_MS);
      },
      onResync: () => {
        invalidateAllForOrg(queryClient, org);
      },
      onDisconnected: () => {
        setIsLive(false);
      },
      onDisabled: () => {
        setIsLive(false);
      },
    });

    return () => {
      if (debounceTimer !== null) clearTimeout(debounceTimer);
      disconnect();
    };
  }, [org, queryClient]);

  return (
    <LiveEventsContext.Provider value={{ isLive }}>
      {children}
    </LiveEventsContext.Provider>
  );
}
