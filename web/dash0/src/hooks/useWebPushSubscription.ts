import { useCallback } from "react";
import { useVapidPublicKey } from "@/api/hooks";

/** Converts a base64url string to a Uint8Array (standard Web Push helper). */
function urlBase64ToUint8Array(base64String: string): Uint8Array<ArrayBuffer> {
  const padding = "=".repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding)
    .replace(/-/g, "+")
    .replace(/_/g, "/");
  const rawData = window.atob(base64);
  const outputArray = new Uint8Array(rawData.length) as Uint8Array<ArrayBuffer>;
  for (let i = 0; i < rawData.length; ++i) {
    outputArray[i] = rawData.charCodeAt(i);
  }
  return outputArray;
}

function sameKey(existing: ArrayBuffer | null, wanted: Uint8Array): boolean {
  if (!existing) return false;
  const a = new Uint8Array(existing);
  return a.length === wanted.length && a.every((v, i) => v === wanted[i]);
}

/**
 * Subscribes with the given VAPID key. A pre-existing subscription bound to a
 * different key (server key regenerated, env keys changed) makes subscribe()
 * fail with InvalidStateError, so it is dropped first.
 */
export async function subscribeWithKey(
  pushManager: Pick<PushManager, "getSubscription" | "subscribe">,
  vapidPublicKey: string,
): Promise<PushSubscription> {
  const key = urlBase64ToUint8Array(vapidPublicKey);
  const existing = await pushManager.getSubscription();
  if (existing && !sameKey(existing.options?.applicationServerKey ?? null, key)) {
    await existing.unsubscribe();
  }
  return pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
}

export function useWebPushSubscription(org: string) {
  const { data: keyData } = useVapidPublicKey(org);
  const vapidPublicKey = keyData?.publicKey;

  const isSupported =
    typeof window !== "undefined" &&
    "serviceWorker" in navigator &&
    "PushManager" in window;

  const subscribe = useCallback(async (): Promise<string | null> => {
    if (!isSupported || !vapidPublicKey) return null;
    const permission = await Notification.requestPermission();
    if (permission !== "granted") return null;
    const reg = await navigator.serviceWorker.ready;
    const sub = await subscribeWithKey(reg.pushManager, vapidPublicKey);
    return JSON.stringify(sub);
  }, [isSupported, vapidPublicKey]);

  const permission =
    typeof Notification !== "undefined"
      ? Notification.permission
      : "default";

  return { isSupported, permission, subscribe };
}
