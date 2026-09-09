import { useEffect, useRef } from "react";
import {
  useCreateNotificationContact,
  useDeleteNotificationContact,
  type NotificationRoute,
} from "@/api/hooks";
import {
  peekOrphanedPushEndpoints,
  takeOrphanedPushEndpoints,
} from "@/lib/service-worker";
import { useWebPushSubscription } from "@/hooks/useWebPushSubscription";

/**
 * webPushEndpoint extracts the push endpoint from a stored `webpush` contact
 * value (the raw `PushSubscription` JSON), or null when the value is not
 * parseable as one. The endpoint is how the SERVER keys a subscription —
 * `notifications/webpush.go` sends to, and prunes by, `subscriptions[].endpoint`
 * — so it is the join key between a stale registration and its stored row.
 */
export function webPushEndpoint(value: string): string | null {
  try {
    const parsed: unknown = JSON.parse(value);
    if (
      parsed &&
      typeof parsed === "object" &&
      typeof (parsed as { endpoint?: unknown }).endpoint === "string"
    ) {
      return (parsed as { endpoint: string }).endpoint;
    }
  } catch {
    // Not JSON (or not a subscription) — nothing to match on.
  }
  return null;
}

/**
 * useWebPushMigration repairs a browser-push contact orphaned by the
 * `/dash0/` → `/d/` base-path move (spec 2026-09-09-01, §5 step 3).
 *
 * Boot already unregistered the legacy-scoped service worker
 * (`lib/service-worker.ts`), which invalidates the subscription bound to it.
 * The server still holds the row keyed by that now-dead endpoint. Here — on the
 * one page that knows the user's contacts — the stale row is REPLACED rather
 * than left to accumulate: subscribe on the new registration, create the
 * replacement contact, delete the stale one.
 *
 * Deliberately quiet: it runs at most once, only when a stashed endpoint
 * actually matches a stored contact, and only when notification permission is
 * ALREADY granted — a repair must never raise a permission prompt the user did
 * not ask for.
 */
export function useWebPushMigration(
  org: string,
  routes: NotificationRoute[] | undefined,
): void {
  const { isSupported, permission, subscribe } = useWebPushSubscription(org);
  const createContact = useCreateNotificationContact(org);
  const deleteContact = useDeleteNotificationContact(org);
  const started = useRef(false);

  useEffect(() => {
    if (started.current || !isSupported || permission !== "granted" || !routes) {
      return;
    }

    const orphaned = peekOrphanedPushEndpoints();
    if (orphaned.length === 0) return;

    const stale = routes.filter(
      (route) =>
        route.contact.type === "webpush" &&
        orphaned.includes(webPushEndpoint(route.contact.value) ?? ""),
    );

    // Nothing of ours to repair: drop the stash so this never runs again.
    if (stale.length === 0) {
      takeOrphanedPushEndpoints();
      return;
    }

    started.current = true;

    void (async () => {
      try {
        const registration = await navigator.serviceWorker.ready;

        // Already re-subscribed (another tab got here first): only the stale
        // rows are left to clear.
        const existing = await registration.pushManager.getSubscription();
        if (!existing) {
          const json = await subscribe();
          if (!json) return;

          await createContact.mutateAsync({
            type: "webpush",
            value: json,
            label: stale[0].contact.label,
          });
        }

        for (const route of stale) {
          await deleteContact.mutateAsync(route.contact.uid);
        }

        takeOrphanedPushEndpoints();
      } catch (err) {
        // Best effort. The server prunes a dead endpoint on its next send
        // anyway (410 Gone -> pruneSubscriptions), so a failure here costs one
        // wasted push attempt, not a broken page.
        console.warn("[solidping] web push migration failed", err);
        started.current = false;
      }
    })();
    // createContact/deleteContact are stable mutation objects from React Query;
    // including them would re-run this effect on every mutation state change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isSupported, permission, routes, subscribe]);
}
