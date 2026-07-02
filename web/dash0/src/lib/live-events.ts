// Fetch-based SSE reader for the org live hint stream
// (GET /api/v1/orgs/:org/events/stream).
//
// EventSource cannot set an Authorization header and tokens must not go in
// URLs, so the stream is read via fetch + ReadableStream with the usual
// Bearer header. Hints carry no data — only which resource kinds changed —
// so the consumer invalidates the matching query caches and refetches over
// the normal REST API.

import { getToken, msSinceLastApiActivity } from "@/api/client";

export interface LiveEventsCallbacks {
  /** Stream established (hello received is not required — headers suffice). */
  onConnected: () => void;
  /** Server hint: the listed resource kinds changed for this org. */
  onHint: (kinds: string[]) => void;
  /** Server asked for a full resync (bus reconnected behind the scenes). */
  onResync: () => void;
  /** Stream dropped; the manager will retry with backoff. */
  onDisconnected: () => void;
  /**
   * Permanent stop: the feature is disabled (404) or access is denied (403).
   * The dashboard silently keeps polling as before.
   */
  onDisabled: () => void;
}

/**
 * Incremental SSE frame parser. Feed it raw text chunks; it emits
 * (event, data) pairs for every complete frame. Handles frames split across
 * chunk boundaries and ignores comment lines (keep-alive pings).
 */
export function createSSEParser(
  onEvent: (event: string, data: string) => void,
): (chunk: string) => void {
  let buffer = "";
  let eventName = "";
  let dataLines: string[] = [];

  const dispatch = () => {
    if (eventName || dataLines.length > 0) {
      onEvent(eventName || "message", dataLines.join("\n"));
    }
    eventName = "";
    dataLines = [];
  };

  return (chunk: string) => {
    buffer += chunk;
    let newlineIdx = buffer.indexOf("\n");
    while (newlineIdx !== -1) {
      let line = buffer.slice(0, newlineIdx);
      buffer = buffer.slice(newlineIdx + 1);
      if (line.endsWith("\r")) line = line.slice(0, -1);

      if (line === "") {
        dispatch(); // blank line terminates a frame
      } else if (line.startsWith(":")) {
        // comment / keep-alive ping — ignored
      } else if (line.startsWith("event:")) {
        eventName = line.slice("event:".length).trimStart();
      } else if (line.startsWith("data:")) {
        dataLines.push(line.slice("data:".length).trimStart());
      }
      // other fields (id:, retry:) are not used by this protocol

      newlineIdx = buffer.indexOf("\n");
    }
  };
}

/** Parses a `hint` event payload; returns [] for malformed data. */
export function parseHintKinds(data: string): string[] {
  try {
    const parsed: unknown = JSON.parse(data);
    const kinds = (parsed as { kinds?: unknown }).kinds;
    if (Array.isArray(kinds)) {
      return kinds.filter((k): k is string => typeof k === "string");
    }
  } catch {
    // fall through
  }
  return [];
}

const BASE_BACKOFF_MS = 1_000;
const MAX_BACKOFF_MS = 30_000;

/** Jittered exponential backoff, capped. Exported for tests. */
export function backoffDelay(attempt: number, random: () => number = Math.random): number {
  const exp = Math.min(BASE_BACKOFF_MS * 2 ** attempt, MAX_BACKOFF_MS);
  const jitter = 0.7 + random() * 0.6; // ±30%
  return Math.round(exp * jitter);
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const id = setTimeout(done, ms);
    function done() {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }
    function onAbort() {
      clearTimeout(id);
      done();
    }
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

/** API traffic must be quiet this long before the stream opens. Slightly
 * above the 500ms window load-completion heuristics (Playwright
 * `networkidle`) need, so they latch before our permanent connection
 * appears. */
const API_QUIET_MS = 700;
/** Never delay the stream longer than this, even on a page that polls
 * continuously. */
const API_QUIET_MAX_WAIT_MS = 5_000;
/** Poll cadence while waiting for the quiet gap. */
const API_QUIET_POLL_MS = 250;

/**
 * Waits until the page's REST traffic has been quiet for a moment (capped).
 * The long-lived stream connection then never competes with first-paint
 * fetches and never prevents load-completion heuristics from settling.
 */
async function waitForApiQuiet(signal: AbortSignal): Promise<void> {
  const start = Date.now();
  while (
    !signal.aborted &&
    msSinceLastApiActivity() < API_QUIET_MS &&
    Date.now() - start < API_QUIET_MAX_WAIT_MS
  ) {
    await sleep(API_QUIET_POLL_MS, signal);
  }
}

/**
 * Opens the live event stream for an org and keeps it open: reconnects with
 * jittered capped backoff on drops, re-reading the (possibly refreshed)
 * token from storage on every attempt. The server closes the stream at
 * access-token expiry; the reconnect then carries the fresh token.
 *
 * Returns a disposer that terminates the connection loop.
 */
export function connectLiveEvents(
  org: string,
  callbacks: LiveEventsCallbacks,
): () => void {
  const controller = new AbortController();
  const { signal } = controller;

  const run = async () => {
    let attempt = 0;
    while (!signal.aborted) {
      const token = getToken();
      if (!token) {
        // Not authenticated (yet) — wait and re-check without hammering.
        await sleep(backoffDelay(attempt), signal);
        continue;
      }

      let connected = false;
      try {
        await waitForApiQuiet(signal);
        if (signal.aborted) break;

        const response = await fetch(`/api/v1/orgs/${org}/events/stream`, {
          headers: {
            Authorization: `Bearer ${token}`,
            Accept: "text/event-stream",
          },
          signal,
        });

        if (response.status === 404 || response.status === 403) {
          // Disabled server-side or no org access: permanent fallback to
          // polling — not an error path.
          callbacks.onDisabled();
          return;
        }
        if (!response.ok || !response.body) {
          throw new Error(`stream unavailable (${response.status})`);
        }

        connected = true;
        attempt = 0;
        callbacks.onConnected();

        const parser = createSSEParser((event, data) => {
          if (event === "hint") {
            const kinds = parseHintKinds(data);
            if (kinds.length > 0) callbacks.onHint(kinds);
          } else if (event === "resync") {
            callbacks.onResync();
          }
          // "hello" carries the protocol version; v1 has nothing to negotiate.
        });

        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          parser(decoder.decode(value, { stream: true }));
        }
      } catch {
        // Network error or aborted — fall through to reconnect handling.
      }

      if (signal.aborted) break;
      if (connected) callbacks.onDisconnected();
      attempt += 1;
      await sleep(backoffDelay(attempt), signal);
    }
  };

  void run();

  return () => {
    controller.abort();
  };
}
