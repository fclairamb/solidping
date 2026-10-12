import { describe, expect, it, vi } from "vitest";

import { subscribeWithKey } from "./useWebPushSubscription";

// base64url of bytes [1,2,3,4]
const KEY = "AQIDBA";

function makePushManager(existingKey: number[] | null) {
  const unsubscribe = vi.fn().mockResolvedValue(true);
  const existing =
    existingKey === null
      ? null
      : {
          options: { applicationServerKey: new Uint8Array(existingKey).buffer },
          unsubscribe,
        };
  const subscribe = vi.fn().mockResolvedValue({ endpoint: "https://push.acme.com/x" });
  const getSubscription = vi.fn().mockResolvedValue(existing);
  return {
    pm: { getSubscription, subscribe } as unknown as PushManager,
    unsubscribe,
    subscribe,
  };
}

// The helper uses window.atob; make it available in a node environment.
if (typeof globalThis.window === "undefined") {
  (globalThis as unknown as { window: unknown }).window = { atob: globalThis.atob };
}

describe("subscribeWithKey", () => {
  it("unsubscribes an existing subscription bound to a different key", async () => {
    const { pm, unsubscribe, subscribe } = makePushManager([9, 9, 9, 9]);
    await subscribeWithKey(pm, KEY);
    expect(unsubscribe).toHaveBeenCalledOnce();
    expect(subscribe).toHaveBeenCalledOnce();
  });

  it("keeps an existing subscription bound to the same key", async () => {
    const { pm, unsubscribe, subscribe } = makePushManager([1, 2, 3, 4]);
    await subscribeWithKey(pm, KEY);
    expect(unsubscribe).not.toHaveBeenCalled();
    expect(subscribe).toHaveBeenCalledOnce();
  });

  it("subscribes directly when there is no subscription", async () => {
    const { pm, unsubscribe, subscribe } = makePushManager(null);
    await subscribeWithKey(pm, KEY);
    expect(unsubscribe).not.toHaveBeenCalled();
    expect(subscribe).toHaveBeenCalledOnce();
  });
});
