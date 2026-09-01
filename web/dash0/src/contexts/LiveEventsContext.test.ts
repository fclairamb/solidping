import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import type {
  LiveEventsCallbacks,
  LiveScope,
  LiveSocketHandle,
} from "@/lib/live-socket";
import {
  LIVE_INVALIDATE_MIN_INTERVAL_MS,
  LIVE_LAZY_POLL_MS,
  LiveRegistry,
  stretchWhileLive,
} from "./LiveEventsContext";

const ORG = "acme";

/** A controllable fake connectLiveSocket: captures the callbacks the
 * registry wired up and every subscribe/unsubscribe frame the registry
 * sent, so tests can drive the registry exactly like the real socket would
 * without a network or WebSocket global. */
class FakeConnection {
  callbacks: LiveEventsCallbacks | null = null;
  sent: Array<{ scope: LiveScope; action: "subscribe" | "unsubscribe" }> = [];
  disconnected = false;

  connect = (_org: string, callbacks: LiveEventsCallbacks): LiveSocketHandle => {
    this.callbacks = callbacks;
    return {
      send: (scope, action) => this.sent.push({ scope, action }),
      disconnect: () => {
        this.disconnected = true;
      },
    };
  };

  open(): void {
    this.callbacks?.onOpen();
  }
  subscribed(scope: LiveScope): void {
    this.callbacks?.onSubscribed(scope);
  }
  update(scope: LiveScope, kinds: string[]): void {
    this.callbacks?.onUpdate(scope, kinds);
  }
  resync(): void {
    this.callbacks?.onResync();
  }
  scopeError(scope: LiveScope, error: { code: string; title: string }): void {
    this.callbacks?.onScopeError(scope, error);
  }
  disconnectedByServer(): void {
    this.callbacks?.onDisconnected();
  }
  disabled(): void {
    this.callbacks?.onDisabled();
  }
}

function seedQueries(client: QueryClient): void {
  const keys: unknown[][] = [
    ["checks", ORG, { limit: 1000 }],
    ["checks", "infinite", ORG, {}],
    ["check-stats", ORG],
    ["check", ORG, "uid-1"],
    ["check", ORG, "uid-2"],
    ["results", ORG, { checkUid: "uid-1" }],
    ["results", ORG, { checkUid: "uid-2" }],
    ["allResults", ORG, {}],
    ["checkAvailability", ORG, "uid-1", ["24h"], undefined],
    ["incidents", ORG, {}],
    ["events", ORG, {}],
    ["jobsStats", ORG, { allOrgs: false }],
    ["backgroundJobs", ORG, {}],
    // Other org + non-org keys must never be touched.
    ["checks", "other-org", {}],
    ["checks", "infinite", "other-org", {}],
    ["features"],
  ];
  for (const key of keys) {
    client.setQueryData(key, { seeded: true });
  }
}

function staleKeys(client: QueryClient): string[] {
  return client
    .getQueryCache()
    .getAll()
    .filter((q) => q.state.isInvalidated)
    .map((q) => JSON.stringify(q.queryKey));
}

function setup() {
  const client = new QueryClient();
  seedQueries(client);
  const conn = new FakeConnection();
  const registry = new LiveRegistry(ORG, client, conn.connect);
  return { client, conn, registry };
}

describe("stretchWhileLive", () => {
  it("keeps the base interval when not live", () => {
    expect(stretchWhileLive(30_000, false)).toBe(30_000);
  });

  it("stretches to the lazy safety net when live", () => {
    expect(stretchWhileLive(30_000, true)).toBe(LIVE_LAZY_POLL_MS);
  });

  it("never shrinks an interval already slower than the safety net", () => {
    expect(stretchWhileLive(LIVE_LAZY_POLL_MS * 2, true)).toBe(LIVE_LAZY_POLL_MS * 2);
  });
});

describe("LiveRegistry subscription refcounting", () => {
  it("sends subscribe only on the 0->1 transition and unsubscribe only on 1->0", () => {
    const { conn, registry } = setup();
    registry.start();
    conn.open();

    const scope: LiveScope = { entity: "checks" };
    registry.addScope(scope);
    registry.addScope(scope); // second interested component
    expect(conn.sent).toEqual([{ scope, action: "subscribe" }]);

    registry.removeScope(scope); // first component unmounts — still refcounted
    expect(conn.sent).toEqual([{ scope, action: "subscribe" }]);

    registry.removeScope(scope); // last component unmounts
    expect(conn.sent).toEqual([
      { scope, action: "subscribe" },
      { scope, action: "unsubscribe" },
    ]);
  });

  it("treats check scopes with different uids as independent subscriptions", () => {
    const { conn, registry } = setup();
    registry.start();
    conn.open();

    registry.addScope({ entity: "check", uid: "uid-1" });
    registry.addScope({ entity: "check", uid: "uid-2" });

    expect(conn.sent).toEqual([
      { scope: { entity: "check", uid: "uid-1" }, action: "subscribe" },
      { scope: { entity: "check", uid: "uid-2" }, action: "subscribe" },
    ]);
  });
});

describe("LiveRegistry replay on (re)connect", () => {
  it("replays every currently-registered scope after onOpen", () => {
    const { conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.addScope({ entity: "incidents" });
    registry.start();

    conn.open(); // first connect
    expect(conn.sent).toEqual(
      expect.arrayContaining([
        { scope: { entity: "checks" }, action: "subscribe" },
        { scope: { entity: "incidents" }, action: "subscribe" },
      ]),
    );

    conn.sent = [];
    conn.open(); // simulated reconnect
    expect(conn.sent).toEqual(
      expect.arrayContaining([
        { scope: { entity: "checks" }, action: "subscribe" },
        { scope: { entity: "incidents" }, action: "subscribe" },
      ]),
    );
  });
});

describe("LiveRegistry scope-accurate invalidation", () => {
  // The subscribed ack starts the scope's hint cooldown, so these tests use
  // fake timers and step past LIVE_INVALIDATE_MIN_INTERVAL_MS before sending
  // an update hint — they exercise routing accuracy, not damping (which has
  // its own suite below).
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("subscribed ack invalidates exactly that scope's default roots", () => {
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "checks" });

    const stale = staleKeys(client);
    expect(stale).toContain(JSON.stringify(["checks", ORG, { limit: 1000 }]));
    expect(stale).not.toContain(JSON.stringify(["incidents", ORG, {}]));
  });

  it("subscribed ack on the checks scope also invalidates the paginated (infinite) checks list", () => {
    // The checks *list page* fetches exclusively through useInfiniteChecks,
    // whose key is ["checks", "infinite", org, options] — org one segment
    // deeper than the flat orgRoot shape. This locks in the infiniteOrgRoot
    // mapping; without it, the list page's live subscription would send the
    // subscribe frame yet never refresh anything the page renders.
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "checks" });

    expect(staleKeys(client)).toContain(JSON.stringify(["checks", "infinite", ORG, {}]));
  });

  it("a 'checks' kind hint (status transition) invalidates the infinite checks list", () => {
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "checks" });
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    conn.update({ entity: "checks" }, ["checks"]);
    expect(staleKeys(client)).toContain(JSON.stringify(["checks", "infinite", ORG, {}]));
  });

  it("a 'checks' kind hint also invalidates check-stats", () => {
    // Spec 2026-09-01-01: check-stats gates the dashboard's empty-org
    // onboarding hero. An MCP- or API-created check publishes kind "checks"
    // (a real membership transition), so that hint must refresh the stats
    // query too — otherwise the hero can sit over a non-empty org for up to
    // LIVE_LAZY_POLL_MS (5 minutes) waiting on the lazy safety-net poll.
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "checks" });
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    conn.update({ entity: "checks" }, ["checks"]);
    expect(staleKeys(client)).toContain(JSON.stringify(["check-stats", ORG]));
  });

  it("a 'results' kind hint does NOT invalidate check-stats", () => {
    // The negative guard for the layer-3 constraint: check-stats must be
    // reachable only through the "checks" kind, never through "results" —
    // that firehose (check workers write results continuously) is exactly
    // what spec 2026-08-09-07 keeps away from org-wide refetches, and
    // wiring check-stats to it would reproduce the same request-rate
    // problem for the dashboard hero gate.
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "checks" });
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    conn.update({ entity: "checks" }, ["results"]);
    expect(staleKeys(client)).not.toContain(JSON.stringify(["check-stats", ORG]));
  });

  it("update on a check scope invalidates only that check's queries, not another check's", () => {
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "check", uid: "uid-1" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "check", uid: "uid-1" });

    const before = staleKeys(client);
    expect(before).toContain(JSON.stringify(["check", ORG, "uid-1"]));
    expect(before).not.toContain(JSON.stringify(["check", ORG, "uid-2"]));

    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    conn.update({ entity: "check", uid: "uid-1" }, ["results"]);
    const stale = staleKeys(client);
    // results for uid-1 (nested checkUid) invalidated, uid-2's left alone.
    expect(stale).toContain(JSON.stringify(["results", ORG, { checkUid: "uid-1" }]));
    expect(stale).not.toContain(JSON.stringify(["results", ORG, { checkUid: "uid-2" }]));
    expect(stale).not.toContain(JSON.stringify(["check", ORG, "uid-2"]));
  });

  it("a 'results' kind update on a check scope also invalidates that check's own detail query", () => {
    // realtime.KindChecks is published only on an actual status transition;
    // a plain result write (the common, steady-state case) publishes only
    // kind "results" — so the check's own detail query (which embeds
    // lastResult/lastStatusChange) must be invalidated on "results" too, or
    // "last checked" goes stale for a whole lazy-poll interval on every
    // check that isn't currently transitioning status.
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "check", uid: "uid-1" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "check", uid: "uid-1" });
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    conn.update({ entity: "check", uid: "uid-1" }, ["results"]);
    const stale = staleKeys(client);
    expect(stale).toContain(JSON.stringify(["check", ORG, "uid-1"]));
    expect(stale).not.toContain(JSON.stringify(["check", ORG, "uid-2"]));
  });

  it("a 'results' kind update on the checks collection scope does NOT invalidate the checks list", () => {
    // Spec 2026-08-09-07: check workers write results continuously, so this
    // hint arrives essentially without pause; refetching the org's whole
    // checks list (lastResult embedded for every check) on each one is what
    // made one open tab worth ~0.5 list requests per second. Result-derived
    // caches still refresh here; the list itself refreshes on a real status
    // transition (kind "checks") and on its own CHECKS_LIST_POLL_MS poll.
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "checks" });
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    conn.update({ entity: "checks" }, ["results"]);
    const stale = staleKeys(client);
    expect(stale).not.toContain(JSON.stringify(["checks", ORG, { limit: 1000 }]));
    expect(stale).not.toContain(JSON.stringify(["checks", "infinite", ORG, {}]));
    // …while the result-derived caches on the same scope still refresh.
    expect(stale).toContain(JSON.stringify(["results", ORG, { checkUid: "uid-1" }]));
    expect(stale).toContain(
      JSON.stringify(["checkAvailability", ORG, "uid-1", ["24h"], undefined])
    );
  });

  it("empty kinds on update means 'all' for that scope's roots", () => {
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "incidents" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "incidents" });
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    conn.update({ entity: "incidents" }, []);
    const stale = staleKeys(client);
    expect(stale).toContain(JSON.stringify(["incidents", ORG, {}]));
  });

  it("resync invalidates every currently subscribed scope once", () => {
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.addScope({ entity: "jobs" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "checks" });
    conn.subscribed({ entity: "jobs" });
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    conn.resync();
    const stale = staleKeys(client);
    expect(stale).toContain(JSON.stringify(["checks", ORG, { limit: 1000 }]));
    expect(stale).toContain(JSON.stringify(["jobsStats", ORG, { allOrgs: false }]));
    expect(stale).not.toContain(JSON.stringify(["incidents", ORG, {}]));
  });

  it("never touches another org's cache entries", () => {
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "checks" });

    expect(staleKeys(client)).not.toContain(JSON.stringify(["checks", "other-org", {}]));
    expect(staleKeys(client)).not.toContain(
      JSON.stringify(["checks", "infinite", "other-org", {}]),
    );
  });
});

describe("LiveRegistry hint damping (refetch storm protection)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  /** Boots a subscribed `checks` scope and clears both the query cache and
   * the cooldown started by the subscribed ack, so each test begins from a
   * quiet, fully-fresh state. */
  function liveChecksScope() {
    const ctx = setup();
    ctx.registry.addScope({ entity: "checks" });
    ctx.registry.start();
    ctx.conn.open();
    ctx.conn.subscribed({ entity: "checks" });
    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    resetAll(ctx.client);
    return ctx;
  }

  function resetAll(client: QueryClient): void {
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));
  }

  // The damping tests below drive "results" hints and assert on a root that
  // kind still owns (checkAvailability) — the checks-list roots deliberately
  // moved off "results" in spec 2026-08-09-07, so asserting them here would
  // test the root mapping instead of the damper.
  it("the first hint after a quiet period invalidates immediately", () => {
    const { client, conn } = liveChecksScope();

    conn.update({ entity: "checks" }, ["results"]);
    expect(staleKeys(client)).toContain(
      JSON.stringify(["checkAvailability", ORG, "uid-1", ["24h"], undefined]),
    );
  });

  it("a burst of hints during the cooldown coalesces into exactly one trailing invalidation", () => {
    const { client, conn } = liveChecksScope();
    const invalidate = vi.spyOn(client, "invalidateQueries");

    conn.update({ entity: "checks" }, ["results"]); // immediate — starts cooldown
    expect(invalidate).toHaveBeenCalledTimes(1);
    resetAll(client);

    // The ~1 s server flush cadence: hints keep arriving inside the cooldown.
    conn.update({ entity: "checks" }, ["results"]);
    vi.advanceTimersByTime(1_000);
    conn.update({ entity: "checks" }, ["results"]);
    vi.advanceTimersByTime(1_000);
    conn.update({ entity: "checks" }, ["results"]);
    expect(invalidate).toHaveBeenCalledTimes(1); // nothing yet — all damped
    expect(staleKeys(client)).toEqual([]);

    // Trailing edge: exactly one deferred invalidation at cooldown expiry.
    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    expect(invalidate).toHaveBeenCalledTimes(2);
    expect(staleKeys(client)).toContain(
      JSON.stringify(["checkAvailability", ORG, "uid-1", ["24h"], undefined]),
    );

    // And nothing else fires afterwards.
    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS * 3);
    expect(invalidate).toHaveBeenCalledTimes(2);
  });

  it("merges kinds across a damped burst so the trailing invalidation covers all hinted roots", () => {
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "check", uid: "uid-1" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "check", uid: "uid-1" });
    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    conn.update({ entity: "check", uid: "uid-1" }, ["incidents"]); // immediate
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    // Two different kinds inside the cooldown — the deferred invalidation
    // must cover both kinds' roots.
    conn.update({ entity: "check", uid: "uid-1" }, ["incidents"]);
    conn.update({ entity: "check", uid: "uid-1" }, ["results"]);
    expect(staleKeys(client)).toEqual([]);

    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    const stale = staleKeys(client);
    expect(stale).toContain(JSON.stringify(["check", ORG, "uid-1"])); // incidents root
    expect(stale).toContain(JSON.stringify(["results", ORG, { checkUid: "uid-1" }])); // results root
  });

  it("an empty-kinds ('all') hint during the cooldown subsumes pending kinds", () => {
    const { client, conn } = liveChecksScope();

    conn.update({ entity: "checks" }, ["checks"]); // immediate
    resetAll(client);

    conn.update({ entity: "checks" }, ["checks"]);
    conn.update({ entity: "checks" }, []); // "all"
    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);

    // The "results" kind's extra roots (checkAvailability) are only covered
    // by the all-kinds expansion — proving the empty hint won.
    expect(staleKeys(client)).toContain(
      JSON.stringify(["checkAvailability", ORG, "uid-1", ["24h"], undefined]),
    );
  });

  it("a hint after the cooldown expires quietly is immediate again", () => {
    const { client, conn } = liveChecksScope();

    conn.update({ entity: "checks" }, ["results"]); // immediate
    resetAll(client);

    // Quiet period: cooldown passes with no further hints, no timer pending.
    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    expect(staleKeys(client)).toEqual([]);

    conn.update({ entity: "checks" }, ["results"]);
    expect(staleKeys(client)).toContain(
      JSON.stringify(["checkAvailability", ORG, "uid-1", ["24h"], undefined]),
    );
  });

  it("onSubscribed catch-up stays immediate even during a hint cooldown", () => {
    const { client, conn } = liveChecksScope();

    conn.update({ entity: "checks" }, ["results"]); // immediate — cooldown running
    resetAll(client);

    // A resubscribe ack (e.g. after a reconnect replay) must not be damped.
    conn.subscribed({ entity: "checks" });
    expect(staleKeys(client)).toContain(JSON.stringify(["checks", ORG, { limit: 1000 }]));
  });

  it("resync stays immediate even during a hint cooldown", () => {
    const { client, conn } = liveChecksScope();

    conn.update({ entity: "checks" }, ["results"]); // immediate — cooldown running
    resetAll(client);

    conn.resync();
    expect(staleKeys(client)).toContain(JSON.stringify(["checks", ORG, { limit: 1000 }]));
  });

  it("an immediate catch-up absorbs pending deferred work (no double invalidation)", () => {
    const { client, conn } = liveChecksScope();
    const invalidate = vi.spyOn(client, "invalidateQueries");

    conn.update({ entity: "checks" }, ["results"]); // immediate (1)
    conn.update({ entity: "checks" }, ["results"]); // damped — deferred scheduled
    conn.subscribed({ entity: "checks" }); // catch-up (2) — clears the deferred work

    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS * 2);
    expect(invalidate).toHaveBeenCalledTimes(2);
  });

  it("stop() cancels pending deferred invalidations", () => {
    const { client, conn, registry } = liveChecksScope();

    conn.update({ entity: "checks" }, ["results"]); // immediate
    resetAll(client);
    conn.update({ entity: "checks" }, ["results"]); // damped — deferred scheduled

    registry.stop();
    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS * 2);
    expect(staleKeys(client)).toEqual([]);
  });

  it("scopes damp independently — one scope's cooldown never delays another's hints", () => {
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.addScope({ entity: "incidents" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "checks" });
    conn.subscribed({ entity: "incidents" });
    vi.advanceTimersByTime(LIVE_INVALIDATE_MIN_INTERVAL_MS);
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    conn.update({ entity: "checks" }, ["results"]); // starts checks' cooldown
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    // incidents' first hint is untouched by checks' running cooldown.
    conn.update({ entity: "incidents" }, []);
    expect(staleKeys(client)).toContain(JSON.stringify(["incidents", ORG, {}]));
  });
});

describe("LiveRegistry poll-stretch gating (per-scope live)", () => {
  it("a scope is not live until its subscribed ack lands", () => {
    const { conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.start();
    conn.open();

    expect(registry.isScopeLive({ entity: "checks" })).toBe(false);
    conn.subscribed({ entity: "checks" });
    expect(registry.isScopeLive({ entity: "checks" })).toBe(true);
  });

  it("a rejected/never-acked subscription keeps the scope reporting not-live", () => {
    const { conn, registry } = setup();
    registry.addScope({ entity: "check", uid: "uid-1" });
    registry.start();
    conn.open();
    // Server never sends `subscribed` (e.g. NOT_FOUND error instead).
    expect(registry.isScopeLive({ entity: "check", uid: "uid-1" })).toBe(false);
  });

  it("disconnect marks every scope not-live and flips the global flag", () => {
    const { conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "checks" });
    expect(registry.getGlobalLive()).toBe(true);
    expect(registry.isScopeLive({ entity: "checks" })).toBe(true);

    conn.disconnectedByServer();
    expect(registry.getGlobalLive()).toBe(false);
    expect(registry.isScopeLive({ entity: "checks" })).toBe(false);
  });

  it("onDisabled marks every scope not-live", () => {
    const { conn, registry } = setup();
    registry.addScope({ entity: "checks" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "checks" });

    conn.disabled();
    expect(registry.getGlobalLive()).toBe(false);
    expect(registry.isScopeLive({ entity: "checks" })).toBe(false);
  });
});

describe("LiveRegistry connection status (sidebar dot)", () => {
  it("starts at 'connecting' before the socket opens", () => {
    const { registry } = setup();
    expect(registry.getStatus()).toBe("connecting");
    registry.start();
    // start() itself doesn't open the socket — still connecting until onOpen.
    expect(registry.getStatus()).toBe("connecting");
  });

  it("walks the full lifecycle: connecting -> live -> drop -> reconnecting -> reconnect -> live", () => {
    const { conn, registry } = setup();
    registry.start();
    expect(registry.getStatus()).toBe("connecting");

    conn.open();
    expect(registry.getStatus()).toBe("live");
    expect(registry.getGlobalLive()).toBe(true);

    conn.disconnectedByServer();
    expect(registry.getStatus()).toBe("reconnecting");
    expect(registry.getGlobalLive()).toBe(false);

    conn.open(); // automatic reconnect succeeds
    expect(registry.getStatus()).toBe("live");
    expect(registry.getGlobalLive()).toBe(true);
  });

  it("'disabled' is terminal and reports gray-equivalent (not live)", () => {
    const { conn, registry } = setup();
    registry.start();
    conn.open();
    expect(registry.getStatus()).toBe("live");

    conn.disabled();
    expect(registry.getStatus()).toBe("disabled");
    expect(registry.getGlobalLive()).toBe(false);

    // Disabled must never bounce back to reconnecting/live — the socket
    // loop has exited server-side (feature off / forbidden).
    conn.disconnectedByServer();
    expect(registry.getStatus()).toBe("disabled");
  });

  it("notifies global listeners on every status transition", () => {
    const { conn, registry } = setup();
    const listener = vi.fn();
    registry.subscribeGlobal(listener);
    registry.start();

    conn.open();
    expect(listener).toHaveBeenCalledTimes(1); // connecting -> live

    conn.disconnectedByServer();
    expect(listener).toHaveBeenCalledTimes(2); // live -> reconnecting

    conn.open();
    expect(listener).toHaveBeenCalledTimes(3); // reconnecting -> live
  });

  it("does not notify listeners for a same-status repeat (idempotent no-op)", () => {
    const { conn, registry } = setup();
    registry.start();
    conn.open();

    const listener = vi.fn();
    registry.subscribeGlobal(listener);

    conn.disconnectedByServer();
    expect(listener).toHaveBeenCalledTimes(1);
    // A second "still down" notification (e.g. a failed reconnect attempt
    // before the next one starts) must not re-fire listeners.
    conn.disconnectedByServer();
    expect(listener).toHaveBeenCalledTimes(1);
  });
});

describe("LiveRegistry.stop", () => {
  it("disconnects the underlying socket", () => {
    const { conn, registry } = setup();
    registry.start();
    registry.stop();
    expect(conn.disconnected).toBe(true);
  });
});

describe("LiveRegistry scope listeners", () => {
  it("notifies a subscribed listener when the scope's live flag flips", () => {
    const { conn, registry } = setup();
    registry.start();
    conn.open();

    const listener = vi.fn();
    const unsubscribe = registry.subscribeScope({ entity: "checks" }, listener);
    registry.addScope({ entity: "checks" });

    conn.subscribed({ entity: "checks" });
    expect(listener).toHaveBeenCalled();

    listener.mockClear();
    unsubscribe();
    conn.disconnectedByServer();
    expect(listener).not.toHaveBeenCalled();
  });
});

describe("LiveRegistry disabled scope (uid not yet known)", () => {
  // The check-detail page passes `undefined` to useLiveSubscription/useScopeLive
  // while the check's canonical uid hasn't resolved yet. There is no scope
  // object at all in that case, so nothing ever reaches addScope — this
  // documents the contract the hooks rely on: a disabled scope never causes a
  // subscribe frame, since the hook's effect body simply doesn't call
  // registry.addScope when passed undefined (see LiveEventsContext.tsx).
  it("never sends subscribe when no scope is ever registered", () => {
    const { conn, registry } = setup();
    registry.start();
    conn.open();

    // Simulates the gated period: the component mounted but the disabled
    // scope means useLiveSubscription's effect never calls addScope.
    expect(conn.sent).toEqual([]);
    expect(registry.isScopeLive({ entity: "check", uid: "uid-1" })).toBe(false);
  });

  it("becomes live once the real uid arrives and the scope is registered", () => {
    const { conn, registry } = setup();
    registry.start();
    conn.open();

    // uid resolves (e.g. the check REST fetch completes) — the caller now
    // registers the real scope.
    registry.addScope({ entity: "check", uid: "uid-1" });
    expect(conn.sent).toEqual([{ scope: { entity: "check", uid: "uid-1" }, action: "subscribe" }]);

    conn.subscribed({ entity: "check", uid: "uid-1" });
    expect(registry.isScopeLive({ entity: "check", uid: "uid-1" })).toBe(true);
  });
});

describe("LiveRegistry per-scope error state (useScopeError)", () => {
  it("onScopeError sets the scope's error and notifies its listeners", () => {
    const { conn, registry } = setup();
    registry.start();
    conn.open();
    registry.addScope({ entity: "check", uid: "slug-not-uid" });

    const listener = vi.fn();
    registry.subscribeScope({ entity: "check", uid: "slug-not-uid" }, listener);

    expect(registry.getScopeError({ entity: "check", uid: "slug-not-uid" })).toBeUndefined();
    conn.scopeError({ entity: "check", uid: "slug-not-uid" }, { code: "NOT_FOUND", title: "Check not found" });

    expect(registry.getScopeError({ entity: "check", uid: "slug-not-uid" })).toEqual({
      code: "NOT_FOUND",
      title: "Check not found",
    });
    expect(listener).toHaveBeenCalled();
  });

  it("a later subscribed ack for the same scope clears the error", () => {
    const { conn, registry } = setup();
    registry.start();
    conn.open();
    registry.addScope({ entity: "check", uid: "uid-1" });

    conn.scopeError({ entity: "check", uid: "uid-1" }, { code: "NOT_FOUND", title: "Check not found" });
    expect(registry.getScopeError({ entity: "check", uid: "uid-1" })).toBeDefined();

    conn.subscribed({ entity: "check", uid: "uid-1" });
    expect(registry.getScopeError({ entity: "check", uid: "uid-1" })).toBeUndefined();
    expect(registry.isScopeLive({ entity: "check", uid: "uid-1" })).toBe(true);
  });

  it("a reconnect (onOpen replay) clears a stale error from the previous connection", () => {
    const { conn, registry } = setup();
    registry.start();
    conn.open();
    registry.addScope({ entity: "check", uid: "uid-1" });
    conn.scopeError({ entity: "check", uid: "uid-1" }, { code: "CONCURRENCY_LIMITED", title: "Subscription limit reached" });
    expect(registry.getScopeError({ entity: "check", uid: "uid-1" })).toBeDefined();

    conn.disconnectedByServer();
    expect(registry.getScopeError({ entity: "check", uid: "uid-1" })).toBeUndefined();

    conn.open(); // automatic reconnect, replays the scope
    expect(registry.getScopeError({ entity: "check", uid: "uid-1" })).toBeUndefined();
  });

  it("onDisabled clears any stale per-scope error", () => {
    const { conn, registry } = setup();
    registry.start();
    conn.open();
    registry.addScope({ entity: "check", uid: "uid-1" });
    conn.scopeError({ entity: "check", uid: "uid-1" }, { code: "INTERNAL_ERROR", title: "Failed to subscribe" });

    conn.disabled();
    expect(registry.getScopeError({ entity: "check", uid: "uid-1" })).toBeUndefined();
  });

  it("errors on one scope never leak into another scope's error state", () => {
    const { conn, registry } = setup();
    registry.start();
    conn.open();
    registry.addScope({ entity: "check", uid: "uid-1" });
    registry.addScope({ entity: "check", uid: "uid-2" });

    conn.scopeError({ entity: "check", uid: "uid-1" }, { code: "NOT_FOUND", title: "Check not found" });
    expect(registry.getScopeError({ entity: "check", uid: "uid-1" })).toBeDefined();
    expect(registry.getScopeError({ entity: "check", uid: "uid-2" })).toBeUndefined();
  });
});
