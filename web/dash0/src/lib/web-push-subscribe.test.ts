import { describe, expect, it, vi } from "vitest";

import {
  type PushManagerLike,
  subscribeWithKey,
  urlBase64ToUint8Array,
} from "./web-push-subscribe";

// Two distinct base64url values standing in for VAPID public keys.
const currentKey = "BAECAwQFBgcICQ";
const oldKey = "BAkICQoLDA0ODw";

function fakeSubscription(key: string | null) {
  return {
    options: {
      applicationServerKey:
        key === null ? null : urlBase64ToUint8Array(key).buffer,
    },
    unsubscribe: vi.fn(async () => true),
  };
}

function fakePushManager(existing: ReturnType<typeof fakeSubscription> | null) {
  const created = { endpoint: "https://push.acme.com/new" };
  const manager = {
    getSubscription: vi.fn(async () => existing),
    subscribe: vi.fn(async () => created),
  };
  return { manager, created };
}

describe("subscribeWithKey", () => {
  it("unsubscribes a subscription bound to another key before subscribing", async () => {
    const existing = fakeSubscription(oldKey);
    const { manager, created } = fakePushManager(existing);

    const sub = await subscribeWithKey(
      manager as unknown as PushManagerLike,
      currentKey,
    );

    expect(existing.unsubscribe).toHaveBeenCalledOnce();
    expect(manager.subscribe).toHaveBeenCalledOnce();
    expect(
      existing.unsubscribe.mock.invocationCallOrder[0],
    ).toBeLessThan(manager.subscribe.mock.invocationCallOrder[0]);
    expect(sub).toBe(created);
  });

  it("keeps a subscription bound to the same key", async () => {
    const existing = fakeSubscription(currentKey);
    const { manager } = fakePushManager(existing);

    await subscribeWithKey(manager as unknown as PushManagerLike, currentKey);

    expect(existing.unsubscribe).not.toHaveBeenCalled();
    expect(manager.subscribe).toHaveBeenCalledOnce();
  });

  it("subscribes directly when there is no subscription yet", async () => {
    const { manager } = fakePushManager(null);

    await subscribeWithKey(manager as unknown as PushManagerLike, currentKey);

    expect(manager.subscribe).toHaveBeenCalledWith({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(currentKey),
    });
  });

  it("drops a subscription whose key the browser does not expose", async () => {
    const existing = fakeSubscription(null);
    const { manager } = fakePushManager(existing);

    await subscribeWithKey(manager as unknown as PushManagerLike, currentKey);

    expect(existing.unsubscribe).toHaveBeenCalledOnce();
  });
});
