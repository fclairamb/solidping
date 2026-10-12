import { useCallback } from "react";
import { useVapidPublicKey } from "@/api/hooks";
import { subscribeWithKey } from "@/lib/web-push-subscribe";

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
