import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// live-socket has no DOM dependency of its own except localStorage (via
// getToken) and window.location (for the ws:// URL) — vitest.config.ts runs
// tests in node env with neither, so both are mocked/stubbed here rather
// than pulling in jsdom for one file.
let currentToken: string | null = "test-token";
vi.mock("@/api/client", () => ({
  getToken: () => currentToken,
  msSinceLastApiActivity: () => Number.MAX_SAFE_INTEGER,
}));

// token-refresh.ts is mocked separately (not through @/api/client) so most
// tests here don't need to care about refresh-token plumbing at all; the
// dedicated "4401" describe block below overrides this mock to assert the
// refresh call actually happens before reconnecting.
const refreshAccessTokenMock = vi.fn<() => Promise<string | null>>(() => Promise.resolve(null));
vi.mock("@/lib/token-refresh", () => ({
  refreshAccessToken: () => refreshAccessTokenMock(),
}));

import {
  backoffDelay,
  connectLiveSocket,
  type LiveEventsCallbacks,
  type LiveScope,
  type WebSocketLike,
} from "./live-socket";

// A controllable fake WebSocket: tests drive onopen/onmessage/onclose
// directly and assert on sent frames, without a real network or a
// browser/jsdom WebSocket global (vitest.config.ts runs in node env).
class FakeSocket implements WebSocketLike {
  onopen: (() => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  sent: string[] = [];
  opened = false;
  closed = false;

  constructor(public url: string) {}

  send(data: string): void {
    // Mimic the browser: send() while CONNECTING throws an InvalidStateError;
    // after close it silently discards. This is what turned an early
    // registry subscribe into a full app crash — keep the fake faithful so
    // the guard in live-socket.ts stays regression-tested.
    if (!this.opened) {
      throw new Error("Failed to execute 'send' on 'WebSocket': Still in CONNECTING state.");
    }
    if (this.closed) return;
    this.sent.push(data);
  }

  close(): void {
    this.closed = true;
    this.onclose?.({ code: 1000, reason: "client close" });
  }

  // Test helpers.
  open(): void {
    this.opened = true;
    this.onopen?.();
  }
  message(payload: unknown): void {
    this.onmessage?.({ data: JSON.stringify(payload) });
  }
  serverClose(code: number, reason = ""): void {
    this.closed = true;
    this.onclose?.({ code, reason });
  }
}

function noopCallbacks(overrides: Partial<LiveEventsCallbacks> = {}): LiveEventsCallbacks {
  return {
    onOpen: vi.fn(),
    onSubscribed: vi.fn(),
    onUpdate: vi.fn(),
    onResync: vi.fn(),
    onScopeError: vi.fn(),
    onDisconnected: vi.fn(),
    onDisabled: vi.fn(),
    ...overrides,
  };
}

describe("backoffDelay", () => {
  it("grows exponentially and caps at 30s", () => {
    const noJitter = () => 0.5; // jitter factor 1.0
    expect(backoffDelay(0, noJitter)).toBe(1_000);
    expect(backoffDelay(1, noJitter)).toBe(2_000);
    expect(backoffDelay(3, noJitter)).toBe(8_000);
    expect(backoffDelay(10, noJitter)).toBe(30_000);
    expect(backoffDelay(30, noJitter)).toBe(30_000);
  });

  it("applies bounded jitter", () => {
    expect(backoffDelay(0, () => 0)).toBe(700);
    expect(backoffDelay(0, () => 1)).toBe(1_300);
  });
});

describe("connectLiveSocket", () => {
  const originalWebSocket = globalThis.WebSocket;

  beforeEach(() => {
    currentToken = "test-token";
    refreshAccessTokenMock.mockClear();
    refreshAccessTokenMock.mockResolvedValue(null);
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    // @ts-expect-error test override
    globalThis.WebSocket = originalWebSocket;
  });

  it("sends auth as the first message once the socket opens", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };

    connectLiveSocket("acme", noopCallbacks(), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));

    sockets[0].open();

    expect(sockets[0].sent).toEqual([JSON.stringify({ type: "auth", token: "test-token" })]);
  });

  it("calls onOpen only after hello, not after the raw socket open", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };
    const onOpen = vi.fn();

    connectLiveSocket("acme", noopCallbacks({ onOpen }), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));

    sockets[0].open();
    expect(onOpen).not.toHaveBeenCalled();

    sockets[0].message({ type: "hello", protocol: 2 });
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  it("routes subscribed/update/resync messages to their callbacks", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };
    const onSubscribed = vi.fn();
    const onUpdate = vi.fn();
    const onResync = vi.fn();

    connectLiveSocket("acme", noopCallbacks({ onSubscribed, onUpdate, onResync }), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].open();
    sockets[0].message({ type: "hello", protocol: 2 });

    sockets[0].message({ type: "subscribed", entity: "check", uid: "u1" });
    expect(onSubscribed).toHaveBeenCalledWith({ entity: "check", uid: "u1" });

    sockets[0].message({ type: "update", entity: "check", uid: "u1", kinds: ["results"] });
    expect(onUpdate).toHaveBeenCalledWith({ entity: "check", uid: "u1" }, ["results"]);

    sockets[0].message({ type: "resync" });
    expect(onResync).toHaveBeenCalledTimes(1);
  });

  it("ignores unknown server message types (forward compat)", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };
    const callbacks = noopCallbacks();

    connectLiveSocket("acme", callbacks, factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].open();
    sockets[0].message({ type: "hello", protocol: 2 });

    expect(() => sockets[0].message({ type: "some-future-type", foo: "bar" })).not.toThrow();
    expect(callbacks.onUpdate).not.toHaveBeenCalled();
  });

  it("an error frame with a recognizable entity invokes onScopeError with the parsed scope and code/title", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };
    const onScopeError = vi.fn();

    connectLiveSocket("acme", noopCallbacks({ onScopeError }), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].open();
    sockets[0].message({ type: "hello", protocol: 2 });

    sockets[0].message({
      type: "error",
      code: "NOT_FOUND",
      title: "Check not found",
      entity: "check",
      uid: "http-api-stonal-io-datalake",
    });

    expect(onScopeError).toHaveBeenCalledWith(
      { entity: "check", uid: "http-api-stonal-io-datalake" },
      { code: "NOT_FOUND", title: "Check not found" },
    );
  });

  it("ignores malformed/unrecognizable error frames (empty entity) without throwing", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };
    const onScopeError = vi.fn();

    connectLiveSocket("acme", noopCallbacks({ onScopeError }), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].open();
    sockets[0].message({ type: "hello", protocol: 2 });

    // Global errors (malformed message / unknown message type) echo an empty
    // entity — not a recognizable scope, must not be reported as a scope error.
    expect(() =>
      sockets[0].message({ type: "error", code: "VALIDATION_ERROR", title: "Malformed message", entity: "", uid: "" }),
    ).not.toThrow();
    expect(onScopeError).not.toHaveBeenCalled();

    // Also tolerate a completely missing entity/code/title.
    expect(() => sockets[0].message({ type: "error" })).not.toThrow();
    expect(onScopeError).not.toHaveBeenCalled();
  });

  it("send() encodes subscribe/unsubscribe frames with entity and uid", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };

    const handle = connectLiveSocket("acme", noopCallbacks(), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].open();
    sockets[0].message({ type: "hello", protocol: 2 });

    const scope: LiveScope = { entity: "check", uid: "u1" };
    handle.send(scope, "subscribe");
    handle.send({ entity: "checks" }, "unsubscribe");

    expect(sockets[0].sent).toEqual([
      JSON.stringify({ type: "auth", token: "test-token" }),
      JSON.stringify({ type: "subscribe", entity: "check", uid: "u1" }),
      JSON.stringify({ type: "unsubscribe", entity: "checks" }),
    ]);
  });

  it("drops send() while the socket is still connecting instead of throwing", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };

    const handle = connectLiveSocket("acme", noopCallbacks(), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));

    // The socket exists but has not opened yet (CONNECTING). A component
    // mounting right now sends a subscribe through the registry — this used
    // to hit WebSocket.send() and crash the whole app with "Failed to
    // execute 'send' on 'WebSocket': Still in CONNECTING state".
    expect(() => handle.send({ entity: "checks" }, "subscribe")).not.toThrow();
    expect(() => handle.send({ entity: "checks" }, "unsubscribe")).not.toThrow();

    sockets[0].open();
    expect(sockets[0].sent).toEqual([JSON.stringify({ type: "auth", token: "test-token" })]);
  });

  it("drops send() between open and hello (server not yet authenticated)", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };

    const handle = connectLiveSocket("acme", noopCallbacks(), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].open();

    // Open but pre-hello: the server's handshake expects `auth` as the
    // first frame, so nothing else may be sent yet.
    handle.send({ entity: "checks" }, "subscribe");
    expect(sockets[0].sent).toEqual([JSON.stringify({ type: "auth", token: "test-token" })]);

    sockets[0].message({ type: "hello", protocol: 2 });
    handle.send({ entity: "checks" }, "subscribe");
    expect(sockets[0].sent).toEqual([
      JSON.stringify({ type: "auth", token: "test-token" }),
      JSON.stringify({ type: "subscribe", entity: "checks" }),
    ]);
  });

  it("drops send() during a reconnect attempt's connecting window, resumes after hello", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };

    const handle = connectLiveSocket("acme", noopCallbacks(), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].open();
    sockets[0].message({ type: "hello", protocol: 2 });

    sockets[0].serverClose(1006, "");
    await vi.advanceTimersByTimeAsync(35_000);
    await vi.waitFor(() => expect(sockets.length).toBeGreaterThanOrEqual(2));

    // Attempt #2 is dialing (CONNECTING) — sends must be dropped, not crash.
    expect(() => handle.send({ entity: "checks" }, "subscribe")).not.toThrow();
    expect(sockets[1].sent).toEqual([]);

    sockets[1].open();
    sockets[1].message({ type: "hello", protocol: 2 });
    handle.send({ entity: "checks" }, "subscribe");
    expect(sockets[1].sent).toEqual([
      JSON.stringify({ type: "auth", token: "test-token" }),
      JSON.stringify({ type: "subscribe", entity: "checks" }),
    ]);
  });

  it("close code 4404 (disabled) stops reconnecting and calls onDisabled", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };
    const onDisabled = vi.fn();

    connectLiveSocket("acme", noopCallbacks({ onDisabled }), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].serverClose(4404, "disabled");

    await vi.waitFor(() => expect(onDisabled).toHaveBeenCalledTimes(1));
    // No second socket is ever created.
    await vi.advanceTimersByTimeAsync(60_000);
    expect(sockets).toHaveLength(1);
  });

  it("close code 4403 (forbidden) stops reconnecting and calls onDisabled", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };
    const onDisabled = vi.fn();

    connectLiveSocket("acme", noopCallbacks({ onDisabled }), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].serverClose(4403, "forbidden");

    await vi.waitFor(() => expect(onDisabled).toHaveBeenCalledTimes(1));
  });

  it("signals onDisconnected even for a close that never authenticated (failed attempt)", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };
    const onDisconnected = vi.fn();

    connectLiveSocket("acme", noopCallbacks({ onDisconnected }), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));

    // No open(), no hello — the attempt never authenticated, e.g. the server
    // was unreachable or closed before the handshake completed. A status
    // consumer derived purely from onOpen/onDisconnected must still be told
    // "still not connected", or it would show "connecting" forever while
    // attempts fail in a loop.
    sockets[0].serverClose(1006, "");
    expect(onDisconnected).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(35_000);
    await vi.waitFor(() => expect(sockets.length).toBeGreaterThanOrEqual(2));
  });

  it("reconnects with backoff on an ordinary disconnect (e.g. 4401 expiry)", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };
    const onDisconnected = vi.fn();

    connectLiveSocket("acme", noopCallbacks({ onDisconnected }), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].open();
    sockets[0].message({ type: "hello", protocol: 2 });

    sockets[0].serverClose(4401, "token expired");
    expect(onDisconnected).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(35_000);
    await vi.waitFor(() => expect(sockets.length).toBeGreaterThanOrEqual(2));
  });

  it("asks the single-flight helper to refresh before reconnecting on a 4401 close", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };
    let resolveRefresh: (token: string | null) => void = () => {};
    refreshAccessTokenMock.mockReturnValue(
      new Promise((resolve) => {
        resolveRefresh = resolve;
      })
    );

    connectLiveSocket("acme", noopCallbacks(), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].open();

    sockets[0].serverClose(4401, "token expired");
    expect(refreshAccessTokenMock).toHaveBeenCalledTimes(1);

    // The reconnect loop must wait for the refresh to settle before it opens
    // a second socket — otherwise the second attempt races the refresh and
    // can carry the same dead token.
    await Promise.resolve();
    expect(sockets).toHaveLength(1);

    resolveRefresh("fresh-token");
    await vi.advanceTimersByTimeAsync(35_000);
    await vi.waitFor(() => expect(sockets.length).toBeGreaterThanOrEqual(2));
  });

  it("re-reads the token from storage on every reconnect attempt", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };

    connectLiveSocket("acme", noopCallbacks(), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].open();

    currentToken = "refreshed-token";
    sockets[0].serverClose(4401, "token expired");

    await vi.advanceTimersByTimeAsync(35_000);
    await vi.waitFor(() => expect(sockets.length).toBeGreaterThanOrEqual(2));
    sockets[1].open();
    expect(sockets[1].sent).toEqual([JSON.stringify({ type: "auth", token: "refreshed-token" })]);
  });

  it("disconnect() aborts the loop and closes the active socket", async () => {
    const sockets: FakeSocket[] = [];
    const factory = (url: string) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    };

    const handle = connectLiveSocket("acme", noopCallbacks(), factory);
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    sockets[0].open();

    handle.disconnect();
    expect(sockets[0].closed).toBe(true);

    await vi.advanceTimersByTimeAsync(60_000);
    expect(sockets).toHaveLength(1);
  });
});
