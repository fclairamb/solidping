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
  closed = false;

  constructor(public url: string) {}

  send(data: string): void {
    this.sent.push(data);
  }

  close(): void {
    this.closed = true;
    this.onclose?.({ code: 1000, reason: "client close" });
  }

  // Test helpers.
  open(): void {
    this.onopen?.();
  }
  message(payload: unknown): void {
    this.onmessage?.({ data: JSON.stringify(payload) });
  }
  serverClose(code: number, reason = ""): void {
    this.onclose?.({ code, reason });
  }
}

function noopCallbacks(overrides: Partial<LiveEventsCallbacks> = {}): LiveEventsCallbacks {
  return {
    onOpen: vi.fn(),
    onSubscribed: vi.fn(),
    onUpdate: vi.fn(),
    onResync: vi.fn(),
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
