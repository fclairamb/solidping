import { describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import type {
  LiveEventsCallbacks,
  LiveScope,
  LiveSocketHandle,
} from "@/lib/live-socket";
import { LIVE_LAZY_POLL_MS, LiveRegistry, stretchWhileLive } from "./LiveEventsContext";

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
    ["check", ORG, "uid-1", {}],
    ["check", ORG, "uid-2", {}],
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

  it("update on a check scope invalidates only that check's queries, not another check's", () => {
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "check", uid: "uid-1" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "check", uid: "uid-1" });

    const before = staleKeys(client);
    expect(before).toContain(JSON.stringify(["check", ORG, "uid-1", {}]));
    expect(before).not.toContain(JSON.stringify(["check", ORG, "uid-2", {}]));

    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

    conn.update({ entity: "check", uid: "uid-1" }, ["results"]);
    const stale = staleKeys(client);
    // results for uid-1 (nested checkUid) invalidated, uid-2's left alone.
    expect(stale).toContain(JSON.stringify(["results", ORG, { checkUid: "uid-1" }]));
    expect(stale).not.toContain(JSON.stringify(["results", ORG, { checkUid: "uid-2" }]));
    expect(stale).not.toContain(JSON.stringify(["check", ORG, "uid-2", {}]));
  });

  it("empty kinds on update means 'all' for that scope's roots", () => {
    const { client, conn, registry } = setup();
    registry.addScope({ entity: "incidents" });
    registry.start();
    conn.open();
    conn.subscribed({ entity: "incidents" });
    client.getQueryCache().getAll().forEach((q) => client.resetQueries({ queryKey: q.queryKey }));

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
