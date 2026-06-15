# Web Push integration: show browser/OS label instead of "Browser"

## Context

The Web Push integration panel (`WebPushChannelPanel` in
[`web/dash0/src/components/integrations/integration-form.tsx`](../../web/dash0/src/components/integrations/integration-form.tsx))
stores each subscription exactly as the browser returns it from `PushManager.subscribe()`.
That raw `PushSubscription` JSON has no `label` field, so the list falls back to the static
`"Browser"` string for every entry:

```tsx
<span className="flex-1 text-sm truncate">{sub.label || "Browser"}</span>
```

When a user has subscribed on two devices (e.g. laptop + phone, or Chrome + Firefox), both
rows show "Browser" and are indistinguishable.

The personal notification routes page (`account.notifications.tsx`) already solves this
correctly: it calls `deriveDeviceLabel()` (lines 74-104) before sending the subscription to
the backend, producing labels like "Chrome on macOS" or "Firefox on Android". The integration
form just lacks this step.

`deriveDeviceLabel` lives only in `account.notifications.tsx`; to reuse it in the integration
form it must be extracted to a shared utility.

## Goals

1. **Extract `deriveDeviceLabel()` to a shared utility** — move or copy the function to
   `web/dash0/src/lib/browser-detection.ts` (or a similarly named file in `src/lib/`) and
   export it. Update `account.notifications.tsx` to import from there.

2. **Stamp the label at subscription time in the integration form** — in
   `WebPushChannelPanel.handleSubscription`, attach `label: deriveDeviceLabel()` to the parsed
   subscription object before appending it to `settings.subscriptions`:

   ```ts
   const handleSubscription = (subscriptionJson: string) => {
     try {
       const parsed = JSON.parse(subscriptionJson) as WebPushSub;
       const already = subs.some((s) => s.endpoint === parsed.endpoint);
       if (!already) {
         onChange({ ...settings, subscriptions: [...subs, { ...parsed, label: deriveDeviceLabel() }] });
       }
     } catch { /* ignore */ }
   };
   ```

3. **Result** — new subscriptions show "Chrome on macOS", "Firefox on Windows",
   "Chrome on Android", etc. instead of "Browser". Existing subscriptions keep their stored
   label (or still fall back to "Browser" if they were added before this fix — no migration
   needed).

## Non-goals

- Migrating or back-filling existing "Browser" entries.
- Switching to a full UA-parsing library (the regex approach in `deriveDeviceLabel` handles the
  common browsers × OS combinations well enough).
- Adding version numbers to the label (e.g. "Chrome 120") — the browser/OS combination is
  sufficient for device identification.

## Verification

- Open the integration edit page for a Web Push integration.
- Click "Enable browser notifications" from two different browsers or devices.
- Each subscription row should show a distinct, human-readable label instead of "Browser".
- Existing subscriptions (stored without a label) still show "Browser" gracefully.
- The personal notification routes page (`/account/notifications`) continues to work correctly,
  importing `deriveDeviceLabel` from the shared utility.
