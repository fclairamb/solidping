# Header live-connection status dot (green/gray/red)

## Problem

The dashboard streams real-time updates over the org live hint WebSocket
(`GET /api/v1/orgs/:org/events/ws`): `LiveEventsProvider` mounts one socket
per org layout (`web/dash0/src/routes/orgs/$org.tsx:858`) and live pages
stretch their polling to a lazy 5-minute safety net
(`web/dash0/src/contexts/LiveEventsContext.tsx:29-37`). But the connection
state is completely invisible. When the socket drops (server restart,
network blip, laptop sleep), the UI silently degrades to polling and the
user has no way to tell whether the numbers on screen are live or minutes
stale. There is also no visible difference between "realtime is off for
this org" and "realtime is broken right now".

Facts about the current plumbing:

- `useLiveStatus()` exposes only a coarse boolean
  (`LiveEventsContext.tsx:356-364`); the registry collapses *disconnected*
  and *disabled* into the same `globalLive = false`
  (`onDisconnected`/`onDisabled` both call `setGlobalLive(false)`,
  `:225-232`).
- `connectLiveSocket` already distinguishes the lifecycle: `hello` →
  `onOpen`, drop → `onDisconnected`, permanent stop (close 4403/4404) →
  `onDisabled` (`web/dash0/src/lib/live-socket.ts:20-36`, `:254-261`), and
  reconnects with jittered backoff capped at 30s (`:65-73`).
- **Gap**: `onDisconnected` only fires when the attempt had authenticated
  (`live-socket.ts:255`). A failed attempt (server unreachable, close
  before `hello`) emits *no callback at all*, so a status derived purely
  from today's callbacks would show "connecting" forever while attempts
  fail in a loop.
- The first connect is deliberately delayed until REST traffic has been
  quiet (up to ~5s, `live-socket.ts:94-115`) — a short non-green period on
  every page load is *normal*, not an error.
- The header's top-right slot is
  `<div className="ml-auto flex items-center gap-1">` holding
  `FeedbackButton` and `CommandMenuTrigger` (`$org.tsx:867-872`), rendered
  inside `LiveEventsProvider`, on every org page (the login/register pages
  return before the header, `:832-834`).

## Product decision

- A small **status dot** at the top right of the header (inside the
  `ml-auto` group, left of the feedback / command-menu buttons), visible on
  every org page, on mobile too (it is only a dot).
- Three colors:
  - **green** — connected: `hello` acked, hints streaming.
  - **red** — broken: the connection dropped or a connect attempt failed;
    the client is retrying with backoff.
  - **gray** — not live *by design*: still connecting (initial API-quiet
    delay), realtime disabled for the deployment (close 4404), or access
    denied (4403). Gray is "no live updates, and that's not an error".
- Hovering shows a tooltip naming the state ("Live updates active",
  "Reconnecting…", "Connecting…", "Live updates unavailable") — i18n in
  all four locales. The dot carries a matching `aria-label`.
- Purely informational: no click action, no popover, no manual reconnect
  (out of scope).
- Status colors follow the existing ok/warning/error conventions (the
  green/red used for check status), *not* `--destructive` (reserved for
  destructive actions).

## Proposal

1. **`live-socket.ts` — signal failed attempts.** Fire `onDisconnected` on
   every socket close that is not a permanent stop, regardless of the
   `authenticated` flag (`live-socket.ts:254-261`). The only consumer is
   the registry, for which an extra "still down" notification is an
   idempotent no-op. Update `live-socket.test.ts` expectations.
2. **`LiveEventsContext.tsx` — four-state status.** Replace the private
   boolean with
   `type LiveConnectionStatus = "connecting" | "live" | "reconnecting" | "disabled"`:
   - registry starts at `"connecting"`; `onOpen` → `"live"`;
     `onDisconnected` → `"reconnecting"`; `onDisabled` → `"disabled"`
     (terminal — the socket loop has exited).
   - `getGlobalLive()` becomes derived (`status === "live"`) so
     `useLiveStatus`/`useScopeLive` and poll stretching are untouched.
   - New hook `useLiveConnectionStatus(): LiveConnectionStatus` via
     `useSyncExternalStore` on the existing global listeners; returns
     `"connecting"` outside the provider (dot simply won't be rendered
     there).
3. **New component** `web/dash0/src/components/layout/live-status-dot.tsx`:
   maps status → color (`live` → green, `reconnecting` → red,
   `connecting`/`disabled` → gray/muted), wrapped in the existing Tooltip
   primitive, `data-testid="live-status-dot"` +
   `data-status={status}` for tests. Per frontend conventions, check the
   design reference first and add the pattern there as part of this change.
4. **Mount** in the header slot (`$org.tsx:867`), before `FeedbackButton`.
5. **i18n**: new keys (e.g. `liveStatus.live`, `liveStatus.connecting`,
   `liveStatus.reconnecting`, `liveStatus.unavailable`) in **all four
   locales** (`src/locales/{en,fr,de,es}`).
6. **Edge cases**:
   - Initial load: gray→green within a few seconds is expected (API-quiet
     delay); red must not appear before the first attempt actually fails.
   - Org switch remounts the provider → dot resets to gray, then green.
   - Login/register pages: no header, no dot (already the case).
   - `disabled` (4404/4403): dot stays gray with the "unavailable" tooltip
     — truthful and low-noise rather than hiding the indicator.

## Out of scope

- Click actions: manual reconnect, diagnostics popover, per-scope liveness
  breakdown.
- Public status page (status0).
- Backend changes — none needed; close codes and the message protocol
  already carry everything.

## Acceptance criteria

- Dot renders at the top right of the header on all org pages (desktop and
  mobile): green while the socket is live, gray while connecting, red after
  a drop or failed attempt, green again after the automatic reconnect.
- With realtime disabled server-side (close 4404), the dot is gray with the
  "unavailable" tooltip and never turns red.
- Tooltip and `aria-label` localized in en/fr/de/es.
- `useLiveStatus`/`useScopeLive` semantics and poll stretching unchanged.
- Unit tests: registry status transitions (connect → live → drop →
  reconnecting → reconnect → live; disabled terminal), and
  `live-socket` now signaling unauthenticated closes.
- Playwright (`e2e/live-updates.spec.ts`): dot present with
  `data-status="live"` once the dashboard is streaming; red-state coverage
  via `page.routeWebSocket` if practical, otherwise unit-level only.
- Design reference page updated with the new pattern.
- `make lint` + `make test-dash` green (no new eslint errors).

## Implementation plan

- [ ] `live-socket.ts`: fire `onDisconnected` on every non-permanent close;
      update `live-socket.test.ts`.
- [ ] `LiveEventsContext.tsx`: `LiveConnectionStatus` state machine,
      derived `getGlobalLive`, new `useLiveConnectionStatus` hook; unit
      tests for transitions.
- [ ] `live-status-dot.tsx` component + header mount in `$org.tsx`;
      add pattern to the design reference.
- [ ] i18n keys in en/fr/de/es.
- [ ] Playwright coverage in `e2e/live-updates.spec.ts`.
- [ ] Run `make lint` + `make test-dash`.
