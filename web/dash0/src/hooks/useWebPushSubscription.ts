import { useCallback } from "react";
import { useVapidPublicKey } from "@/api/hooks";

/** Converts a base64url string to a Uint8Array (standard Web Push helper). */
function urlBase64ToUint8Array(base64String: string): Uint8Array {
  const padding = "=".repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding)
    .replace(/-/g, "+")
    .replace(/_/g, "/");
  const rawData = window.atob(base64);
  const outputArray = new Uint8Array(rawData.length);
  for (let i = 0; i < rawData.length; ++i) {
    outputArray[i] = rawData.charCodeAt(i);
  }
  return outputArray;
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
    const sub = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(vapidPublicKey),
    });
    return JSON.stringify(sub);
  }, [isSupported, vapidPublicKey]);

  const permission =
    typeof Notification !== "undefined"
      ? Notification.permission
      : "default";

  return { isSupported, permission, subscribe };
}
