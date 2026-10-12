/** Converts a base64url string to a Uint8Array (standard Web Push helper). */
export function urlBase64ToUint8Array(
  base64String: string,
): Uint8Array<ArrayBuffer> {
  const padding = "=".repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding)
    .replace(/-/g, "+")
    .replace(/_/g, "/");
  const rawData = atob(base64);
  const outputArray = new Uint8Array(rawData.length) as Uint8Array<ArrayBuffer>;
  for (let i = 0; i < rawData.length; ++i) {
    outputArray[i] = rawData.charCodeAt(i);
  }
  return outputArray;
}

function sameKey(
  existing: ArrayBuffer | null | undefined,
  current: Uint8Array<ArrayBuffer>,
): boolean {
  if (!existing || existing.byteLength !== current.byteLength) return false;
  const bytes = new Uint8Array(existing);
  return bytes.every((byte, i) => byte === current[i]);
}

/** The part of `PushManager` the subscribe flow uses (mockable in tests). */
export type PushManagerLike = Pick<
  PushManager,
  "getSubscription" | "subscribe"
>;

/**
 * subscribeWithKey subscribes this browser to push with the server's current
 * VAPID public key.
 *
 * A subscription made with another key (the server's key changed, or the
 * operator rotated the env keys) cannot be reused: the push service rejects
 * pushes signed with the new key, and `subscribe` refuses a different key
 * while the old subscription exists. So an existing subscription bound to a
 * different key is dropped first. One bound to the same key is kept, and
 * `subscribe` returns it unchanged.
 */
export async function subscribeWithKey(
  pushManager: PushManagerLike,
  vapidPublicKey: string,
): Promise<PushSubscription> {
  const applicationServerKey = urlBase64ToUint8Array(vapidPublicKey);
  const existing = await pushManager.getSubscription();
  if (
    existing &&
    !sameKey(existing.options?.applicationServerKey, applicationServerKey)
  ) {
    await existing.unsubscribe();
  }
  return pushManager.subscribe({ userVisibleOnly: true, applicationServerKey });
}
