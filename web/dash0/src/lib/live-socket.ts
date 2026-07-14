// WebSocket client for the org live hint socket
// (GET /api/v1/orgs/:org/events/ws), replacing the SSE reader in
// live-events.ts. A connection receives nothing until it subscribes to a
// scope — hints carry no data, only which resource kind changed for which
// scope, so the consumer invalidates the matching query caches and refetches
// over the normal REST API.

import { getExpiresAt, getToken, msSinceLastApiActivity } from "@/api/client";
import { refreshWithOutcome } from "@/lib/token-refresh";

/** Scopes the client can subscribe to. `check` is the only per-uid entity;
 * the rest are org-collection scopes (the org is implied by the socket). */
export type LiveEntity = "check" | "checks" | "incidents" | "events" | "jobs";

export interface LiveScope {
  entity: LiveEntity;
  /** Required for entity "check"; must be omitted for collection scopes. */
  uid?: string;
}

export interface LiveEventsCallbacks {
  /** Auth accepted (`hello` received). The socket is ready for subscribe/unsubscribe. */
  onOpen: () => void;
  /** Server acked a subscribe for this scope. */
  onSubscribed: (scope: LiveScope) => void;
  /** Server hint: the given scope changed with these kinds (possibly empty = "all"). */
  onUpdate: (scope: LiveScope, kinds: string[]) => void;
  /** Server asked for a full resync (bus transport gap): invalidate every subscribed scope once. */
  onResync: () => void;
  /**
   * Server rejected a subscribe/unsubscribe for this scope (`error` frame
   * that echoes a recognizable `entity`, e.g. NOT_FOUND for a slug used where
   * only a UID resolves). Per-message and non-fatal — the socket stays open
   * and other scopes are unaffected. Frames that don't echo a recognizable
   * entity (malformed-message / unknown-type errors) are not reported here.
   */
  onScopeError: (scope: LiveScope, error: { code: string; title: string }) => void;
  /** Socket dropped; the manager will retry with backoff. */
  onDisconnected: () => void;
  /**
   * Permanent stop: the feature is disabled (close 4404) or access is denied
   * (close 4403). The dashboard silently keeps polling as before.
   */
  onDisabled: () => void;
}

/** Server->client message shape (subset of fields used, by type). */
interface ServerMessage {
  type: string;
  entity?: string;
  uid?: string;
  kinds?: unknown;
  code?: string;
  title?: string;
}

const LIVE_ENTITIES: readonly LiveEntity[] = [
  "check",
  "checks",
  "incidents",
  "events",
  "jobs",
];

function isLiveEntity(value: unknown): value is LiveEntity {
  return (
    typeof value === "string" &&
    (LIVE_ENTITIES as readonly string[]).includes(value)
  );
}

function scopeKey(scope: LiveScope): string {
  return scope.uid ? `${scope.entity}:${scope.uid}` : scope.entity;
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

/** API traffic must be quiet this long before the socket opens. Slightly
 * above the 500ms window load-completion heuristics (Playwright
 * `networkidle`) need, so they latch before our permanent connection
 * appears. */
const API_QUIET_MS = 700;
/** Never delay the socket longer than this, even on a page that polls
 * continuously. */
const API_QUIET_MAX_WAIT_MS = 5_000;
/** Poll cadence while waiting for the quiet gap. */
const API_QUIET_POLL_MS = 250;

/**
 * Waits until the page's REST traffic has been quiet for a moment (capped).
 * The long-lived socket connection then never competes with first-paint
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

/** Close codes the server sends for permanent-stop conditions (see
 * server/internal/handlers/realtimews). Any other close code (including the
 * generic 1006 a browser reports for network drops) reconnects with backoff. */
const CLOSE_FORBIDDEN = 4403;
const CLOSE_DISABLED = 4404;
/** The server closes the socket with this code when the access token used
 * to authenticate it has expired. Refresh before reconnecting so the next
 * attempt's getToken() read (in the run() loop) picks up a live token
 * instead of looping on the same dead one with backoff. */
const CLOSE_TOKEN_EXPIRED = 4401;

/** Minimal shape of the WebSocket constructor this client depends on —
 * lets tests inject a fake implementation without a browser/jsdom runtime. */
export type WebSocketFactory = (url: string) => WebSocketLike;

export interface WebSocketLike {
  onopen: (() => void) | null;
  onclose: ((ev: { code: number; reason: string }) => void) | null;
  onerror: (() => void) | null;
  onmessage: ((ev: { data: string }) => void) | null;
  send(data: string): void;
  close(): void;
}

function defaultFactory(url: string): WebSocketLike {
  return new WebSocket(url) as unknown as WebSocketLike;
}

/** A live connection's public send surface: subscribe/unsubscribe. Frames
 * are silently dropped unless the socket is authenticated (`hello`
 * received) — safe because the caller replays every active scope in onOpen
 * after each (re)connect, and an unsubscribe for a scope the connection
 * never subscribed to needs no undoing server-side. */
export interface LiveSocketHandle {
  send: (scope: LiveScope, action: "subscribe" | "unsubscribe") => void;
  /** Terminates the connection loop. */
  disconnect: () => void;
}

/**
 * Opens the live hint WebSocket for an org and keeps it open: reconnects
 * with jittered capped backoff on drops. Authentication happens at the HTTP
 * level — the browser attaches the `access_token` cookie to the same-origin
 * handshake automatically, so there is no in-band `auth` frame. The run() loop
 * still re-reads the stored token on every attempt to skip dialing while
 * logged out and to refresh a known-dead token before dialing (which re-mints
 * the cookie the handshake relies on). The server closes the socket at
 * access-token expiry (4401); the caller replays its subscriptions via onOpen.
 */
export function connectLiveSocket(
  org: string,
  callbacks: LiveEventsCallbacks,
  factory: WebSocketFactory = defaultFactory,
): LiveSocketHandle {
  const controller = new AbortController();
  const { signal } = controller;

  // Only ever set between `hello` and the close of that same socket — never
  // while CONNECTING (WebSocket.send() throws "Still in CONNECTING state"
  // there, and a registry-driven subscribe can land in exactly that window:
  // a component mounting while the socket dials) and never before the server
  // acked the connection with `hello` (the earliest point a frame may be sent).
  let activeSocket: WebSocketLike | null = null;

  const send = (scope: LiveScope, action: "subscribe" | "unsubscribe") => {
    if (!activeSocket) return; // not authenticated yet — drop; onOpen replays scopes
    const payload: Record<string, string> = { type: action, entity: scope.entity };
    if (scope.uid) payload.uid = scope.uid;
    activeSocket.send(JSON.stringify(payload));
  };

  const run = async () => {
    let attempt = 0;
    while (!signal.aborted) {
      const token = getToken();
      if (!token) {
        // Not authenticated (yet) — wait and re-check without hammering.
        await sleep(backoffDelay(attempt), signal);
        continue;
      }

      const expiresAt = getExpiresAt();
      if (expiresAt !== null && expiresAt <= Date.now()) {
        // Known-dead token (exp already past — e.g. this tab was suspended
        // through one or more access-token lifetimes). Refresh before dialing:
        // the refresh endpoint re-sets the access_token cookie, so the browser
        // attaches a live cookie to the handshake instead of burning a socket
        // attempt on a token the server will just 401 (see spec Evidence: 139
        // dead-token attempts in one 70-minute HAR capture).
        const refreshed = await refreshWithOutcome();
        if (!refreshed.accessToken) {
          if (refreshed.failureReason === "network-error") {
            attempt += 1;
            await sleep(backoffDelay(attempt), signal);
            continue;
          }
          // A non-network failure has already cleared the session and
          // redirected to login (token-refresh.ts's escalate()) — give up
          // instead of reconnecting with a token already known to be dead.
          callbacks.onDisabled();
          return;
        }
        // refreshed.accessToken is intentionally not read here: the refresh's
        // side effect (re-setting the access_token cookie) is what the
        // cookie-authenticated handshake relies on.
      }

      await waitForApiQuiet(signal);
      if (signal.aborted) break;

      const outcome = await connectOnce(org, factory, callbacks, signal, (sock) => {
        activeSocket = sock;
      });
      activeSocket = null;

      if (signal.aborted) break;
      if (outcome === "disabled") {
        callbacks.onDisabled();
        return;
      }

      attempt += 1;
      await sleep(backoffDelay(attempt), signal);
    }
  };

  void run();

  return {
    send,
    disconnect: () => {
      controller.abort();
      activeSocket?.close();
    },
  };
}

type ConnectOutcome = "disconnected" | "disabled";

/** Opens one socket attempt and resolves once it closes (or the caller
 * aborts). Resolves "disabled" only for a 4403/4404 close — every other
 * outcome (network drop, 4401 expiry, server restart 1012) is
 * "disconnected" and the caller reconnects with backoff.
 *
 * Authentication is at the HTTP level: the browser attaches the access_token
 * cookie to the same-origin handshake automatically, so there is no in-band
 * auth frame — the client just waits for the server's `hello`.
 *
 * `onAuthenticated` fires when the server acks the connection (`hello`), just
 * before callbacks.onOpen — the earliest point the socket may carry caller
 * frames. */
function connectOnce(
  org: string,
  factory: WebSocketFactory,
  callbacks: LiveEventsCallbacks,
  signal: AbortSignal,
  onAuthenticated: (sock: WebSocketLike) => void,
): Promise<ConnectOutcome> {
  return new Promise((resolve) => {
    let settled = false;

    const finish = (outcome: ConnectOutcome) => {
      if (settled) return;
      settled = true;
      signal.removeEventListener("abort", onAbort);
      resolve(outcome);
    };

    const url = wsURL(org);
    const sock = factory(url);

    function onAbort() {
      sock.close();
    }
    signal.addEventListener("abort", onAbort, { once: true });

    // No sock.onopen handler: authentication is at the HTTP level (the browser
    // sent the access_token cookie with the handshake), so nothing is sent on
    // open — the client waits for the server's `hello`.

    sock.onerror = () => {
      // onclose always follows onerror for browser WebSockets; nothing to do here.
    };

    sock.onclose = (ev) => {
      if (ev.code === CLOSE_FORBIDDEN || ev.code === CLOSE_DISABLED) {
        finish("disabled");
        return;
      }

      // Signal every non-permanent close, including a failed attempt that
      // never authenticated (server unreachable, close before `hello`) — a
      // connection-status consumer derived purely from onOpen/onDisconnected
      // would otherwise show "connecting" forever while attempts fail in a
      // loop. The registry is the only consumer and treats a repeat
      // "still down" notification as an idempotent no-op.
      callbacks.onDisconnected();

      if (ev.code === CLOSE_TOKEN_EXPIRED) {
        const expiresAt = getExpiresAt();
        if (expiresAt !== null && expiresAt > Date.now()) {
          // The access token still has time left on it, yet the server sent a
          // 4401: this is a transient/server-side close (proxy hiccup, restart,
          // an out-of-band revocation a fresh dial may not repeat), not a dead
          // session. Reconnect with the current token rather than tearing the
          // session down — if it later genuinely expires, the run() loop's
          // pre-dial check refreshes it (and gives up there if that refresh is
          // refused). Only a token we can't prove is still live gets the
          // refresh-or-give-up treatment below.
          finish("disconnected");
          return;
        }
        // Token is expired (or its liveness is unknown): refresh before the
        // run() loop's next getToken() read, so the next attempt carries a live
        // token instead of looping on the dead one. A refresh that fails for a
        // non-network reason (no refresh token, revoked session) means the
        // session is genuinely over — give up instead of retrying the same dead
        // token at the backoff cap forever (see spec Evidence: 139 useless
        // reconnects in 70 minutes from one zombie tab). token-refresh.ts's
        // escalate() has already cleared the session and redirected to login in
        // that case.
        void refreshWithOutcome().then((outcome) => {
          finish(outcome.accessToken || outcome.failureReason === "network-error"
            ? "disconnected"
            : "disabled");
        });
        return;
      }

      finish("disconnected");
    };

    sock.onmessage = (ev) => {
      let msg: ServerMessage;
      try {
        msg = JSON.parse(ev.data) as ServerMessage;
      } catch {
        return; // malformed frame — ignore
      }

      switch (msg.type) {
        case "hello":
          // Ordering matters: expose the socket for sends first, so the
          // onOpen subscription replay reaches an authenticated socket.
          onAuthenticated(sock);
          callbacks.onOpen();
          break;
        case "subscribed":
          if (isLiveEntity(msg.entity)) {
            callbacks.onSubscribed({ entity: msg.entity, uid: msg.uid });
          }
          break;
        case "update":
          if (isLiveEntity(msg.entity)) {
            const kinds = Array.isArray(msg.kinds)
              ? msg.kinds.filter((k): k is string => typeof k === "string")
              : [];
            callbacks.onUpdate({ entity: msg.entity, uid: msg.uid }, kinds);
          }
          break;
        case "resync":
          callbacks.onResync();
          break;
        case "error":
          // Per-message and non-fatal — the socket stays open. Only report
          // errors that echo a recognizable entity (NOT_FOUND,
          // VALIDATION_ERROR, CONCURRENCY_LIMITED, INTERNAL_ERROR from
          // handleSubscribe); the global "malformed message"/"unknown type"
          // errors echo an empty entity and are silently dropped here, same
          // as today.
          if (isLiveEntity(msg.entity) && typeof msg.code === "string" && typeof msg.title === "string") {
            callbacks.onScopeError(
              { entity: msg.entity, uid: msg.uid },
              { code: msg.code, title: msg.title },
            );
          }
          break;
        default:
          // Unknown server->client type (forward compat) — ignored.
          break;
      }
    };
  });
}

// wsURL is only ever read by the real `defaultFactory` — unit tests inject
// their own WebSocketFactory and never dereference `window`, so this stays
// safe to call from a non-browser (node) test environment as long as no
// caller other than defaultFactory relies on its return value there.
function wsURL(org: string): string {
  if (typeof window === "undefined") return `ws://unknown/api/v1/orgs/${org}/events/ws`;
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}/api/v1/orgs/${org}/events/ws`;
}

/** Encodes a scope as the stable key used to dedupe/replay subscriptions. Exported for the registry. */
export { scopeKey };
